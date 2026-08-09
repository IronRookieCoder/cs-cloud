package workflowrunner

import (
	"context"
	"encoding/json"
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
// runs) — the driver must forward it to the server as a real CompleteTask.
func TestDriverForwardsCompletionSignaledDuringFailCallback(t *testing.T) {
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
	// be accepted (2xx → CLI exit 0) AND actually reach the server — even
	// though execute is still stuck in the fail callback.
	if err := d.SignalTaskCompletion("task-late-complete", agent.CompletionSignal{
		Action: "complete", Summary: "late done",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	waitFor(t, "late /complete forward", func() bool {
		_, ok := fm.taskCallback("/complete")
		return ok
	})

	body, _ := fm.taskCallback("/complete")
	var complete struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(body, &complete); err != nil {
		t.Fatalf("complete body: %v", err)
	}
	if complete.Output != "late done" {
		t.Errorf("complete output = %q, want %q", complete.Output, "late done")
	}
}

// A signal that latches in the narrow window between runAgent's error return
// and the fail path's late mark would never be consumed; marking late must
// flush an already-latched signal.
func TestMarkCompletionLateForwardsLatchedSignal(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, time.Minute, &failingSessionRunner{err: agent.ErrIncomplete}, nil)

	d.registerCompletion("t-latched", "sess-1", "/tmp/wd")
	if err := d.SignalTaskCompletion("t-latched", agent.CompletionSignal{
		Action: "complete", Summary: "latched summary",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("unexpected /complete before late mark")
	}

	d.markCompletionLate("t-latched")

	waitFor(t, "latched signal forward", func() bool {
		_, ok := fm.taskCallback("/complete")
		return ok
	})
	body, _ := fm.taskCallback("/complete")
	var complete struct {
		Output  string `json:"output"`
		Session string `json:"session_id"`
		WorkDir string `json:"work_dir"`
	}
	if err := json.Unmarshal(body, &complete); err != nil {
		t.Fatalf("complete body: %v", err)
	}
	if complete.Output != "latched summary" {
		t.Errorf("complete output = %q, want %q", complete.Output, "latched summary")
	}
	if complete.Session != "sess-1" || complete.WorkDir != "/tmp/wd" {
		t.Errorf("complete session/work_dir = %q/%q, want sess-1//tmp/wd", complete.Session, complete.WorkDir)
	}
}

// After a late forward, further signals are idempotent no-ops: exactly one
// /complete reaches the server.
func TestLateCompletionForwardedOnce(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, time.Minute, &failingSessionRunner{err: agent.ErrIncomplete}, nil)

	d.registerCompletion("t-once", "sess-1", "/tmp/wd")
	d.markCompletionLate("t-once")

	for i := 0; i < 2; i++ {
		if err := d.SignalTaskCompletion("t-once", agent.CompletionSignal{
			Action: "complete", Summary: "done",
		}); err != nil {
			t.Fatalf("SignalTaskCompletion %d: %v", i, err)
		}
	}

	waitFor(t, "late /complete forward", func() bool {
		_, ok := fm.taskCallback("/complete")
		return ok
	})
	time.Sleep(50 * time.Millisecond)
	if got := fm.taskCallbackCount("/complete"); got != 1 {
		t.Fatalf("complete callback count = %d, want 1", got)
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
