package workflowrunner

import (
	"context"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/workflow"
)

// failingSessionRunner simulates the incident class where the driver-side
// session run ends prematurely (stream EOF misjudgment) while the agent keeps
// working autonomously inside csc serve.
type failingSessionRunner struct {
	err error
}

func (r *failingSessionRunner) RunSession(context.Context, string, string, string, []string, string) ([]byte, error) {
	return nil, r.err
}

// Reproduces the 2026-08-06 incident: runAgent returns ErrIncomplete while the
// agent is still working, execute enters failTask, and the fail callback
// wedges. The agent's later "complete task" signal must not disappear into the
// completion registry (runAgent already returned, so popCompletionSignal never
// runs) — the driver must persist it to the outbox so the server can arbitrate.
func TestDriverPersistsCompletionSignaledDuringFailCallback(t *testing.T) {
	runner := &failingSessionRunner{err: agent.ErrIncomplete}
	d, fm := newCSCSessionTestDriver(t, time.Minute, runner, nil)
	fm.gateFailCallbacks()
	t.Cleanup(fm.releaseFailCallbacks)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID: "task-late-complete", WorkspaceID: "ws-1", NodeRunID: "nr-1", AgentID: "agent-1",
		Agent: "csc", Prompt: "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	// Wait until execute is inside the wedged /fail HTTP call.
	select {
	case <-fm.failCallbackStarted():
	case <-time.After(3 * time.Second):
		t.Fatal("fail callback did not start")
	}

	// The agent finishes its real work and signals completion. The signal must
	// be accepted (2xx → CLI exit 0) and durably written to the outbox; the
	// server arbitrates the complete-after-fail race.
	if err := d.SignalTaskCompletion("task-late-complete", agent.CompletionSignal{
		Action: "complete", Summary: "late done",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	pending, err := d.Outbox().Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var complete *OutboxFact
	for i := range pending {
		if pending[i].Kind == "complete" && pending[i].TaskID == "task-late-complete" {
			complete = &pending[i]
			break
		}
	}
	if complete == nil {
		t.Fatal("complete fact not found in outbox")
	}
	if complete.Output != "late done" {
		t.Errorf("complete output = %q, want %q", complete.Output, "late done")
	}

	// The legacy direct HTTP /complete forward must not fire; outbox is the
	// only late path.
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("unexpected legacy /complete HTTP callback")
	}
}

// A signal that arrives after the task has entered the terminal-failure path
// is persisted to the outbox with the original session and workdir so the
// server can preserve the resume pointer.
func TestFailedCompletionStatePersistsLateSignal(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, time.Minute, &failingSessionRunner{err: agent.ErrIncomplete}, nil)

	d.registerCompletion("t-latched", "sess-1", "/tmp/wd")
	d.mu.Lock()
	d.completion["t-latched"].failed = true
	d.mu.Unlock()

	if err := d.SignalTaskCompletion("t-latched", agent.CompletionSignal{
		Action: "complete", Summary: "latched summary",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	pending, err := d.Outbox().Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var complete *OutboxFact
	for i := range pending {
		if pending[i].Kind == "complete" && pending[i].TaskID == "t-latched" {
			complete = &pending[i]
			break
		}
	}
	if complete == nil {
		t.Fatal("complete fact not found in outbox")
	}
	if complete.Output != "latched summary" {
		t.Errorf("complete output = %q, want %q", complete.Output, "latched summary")
	}
	if complete.SessionID != "sess-1" || complete.WorkDir != "/tmp/wd" {
		t.Errorf("complete session/work_dir = %q/%q, want sess-1//tmp/wd", complete.SessionID, complete.WorkDir)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("unexpected legacy /complete HTTP callback")
	}
}

// Late completion signals are intentionally not deduplicated on the device:
// each signal gets its own fact_id and is delivered to the server, where
// completed is an absorbing state.
func TestLateCompletionNotDeduplicated(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, time.Minute, &failingSessionRunner{err: agent.ErrIncomplete}, nil)

	d.registerCompletion("t-once", "sess-1", "/tmp/wd")
	d.mu.Lock()
	d.completion["t-once"].failed = true
	d.mu.Unlock()

	for i := 0; i < 2; i++ {
		if err := d.SignalTaskCompletion("t-once", agent.CompletionSignal{
			Action: "complete", Summary: "done",
		}); err != nil {
			t.Fatalf("SignalTaskCompletion %d: %v", i, err)
		}
	}

	pending, err := d.Outbox().Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	var completeCount int
	for _, f := range pending {
		if f.Kind == "complete" && f.TaskID == "t-once" {
			completeCount++
		}
	}
	if completeCount != 2 {
		t.Fatalf("complete fact count = %d, want 2", completeCount)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("unexpected legacy /complete HTTP callback")
	}
}

// A completion signal for a task the driver is not running still fails loudly
// (handler maps this to 409 so the CLI exits non-zero).
func TestSignalTaskCompletionUnknownTaskErrors(t *testing.T) {
	d, _ := newCSCSessionTestDriver(t, time.Minute, &failingSessionRunner{err: agent.ErrIncomplete}, nil)

	if err := d.SignalTaskCompletion("t-unknown", agent.CompletionSignal{
		Action: "complete", Summary: "done",
	}); err == nil {
		t.Fatal("expected error for unknown task, got nil")
	}
}
