package workflowrunner

import (
	"fmt"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

// completionState tracks an explicit "complete task" signal for a running csc
// task. The agent invokes the complete tool via the localserver handler, which
// calls SignalTaskCompletion; that stores the payload and closes notify so
// runAgent can stop waiting on the session and report completion.
type completionState struct {
	notify  chan struct{}
	payload agent.CompletionSignal
	set     bool
	// failed is set when execute enters the terminal-failure path. The
	// completion registry is kept alive until execute returns so that any
	// complete signal arriving before teardown can be persisted to the outbox
	// instead of being dropped. The server arbitrates the resulting
	// fail/complete race; the device only records the fact.
	failed    bool
	sessionID string
	workDir   string
}

// registerCompletion creates the completion state for a task (csc only). It is
// called by execute before runAgent so the localserver handler can signal the
// agent's explicit completion while the session is still busy. sessionID and
// workDir are kept so a late completion can be written to the outbox with the
// same resume pointer the normal path would report.
func (d *Driver) registerCompletion(taskID, sessionID, workDir string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completion == nil {
		d.completion = make(map[string]*completionState)
	}
	d.completion[taskID] = &completionState{
		notify:    make(chan struct{}),
		sessionID: sessionID,
		workDir:   workDir,
	}
}

// unregisterCompletion removes the completion state for a task. Called by
// execute after runAgent returns.
func (d *Driver) unregisterCompletion(taskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.completion, taskID)
}

// completionNotify returns the notify channel for runAgent's select, or nil if
// no completion state is registered for the task (non-csc, or not running).
func (d *Driver) completionNotify(taskID string) <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cs, ok := d.completion[taskID]; ok {
		return cs.notify
	}
	return nil
}

// popCompletionSignal returns the explicit-completion payload if the agent
// signaled one for this task. Called by execute after runAgent reports a
// nil-error (csc) completion to decide whether to use the tool payload.
func (d *Driver) popCompletionSignal(taskID string) (agent.CompletionSignal, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	cs, ok := d.completion[taskID]
	if !ok || !cs.set {
		return agent.CompletionSignal{}, false
	}
	return cs.payload, true
}

// SignalTaskCompletion records the agent's explicit completion of a task and
// wakes runAgent so it can stop the session. Called by the localserver
// "complete task" endpoint. It is idempotent: a second call for an already
// completed task is a no-op (the task is completing via the first signal).
// When the task has already entered the terminal-failure path, the signal is
// persisted to the outbox; the server arbitrates the fail/complete race.
// Returns an error if the task is not running (already finished / unknown) so
// the handler can respond 409.
func (d *Driver) SignalTaskCompletion(taskID string, sig agent.CompletionSignal) error {
	d.mu.Lock()
	cs, ok := d.completion[taskID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("task %s is not running", taskID)
	}
	if cs.failed {
		// The task has entered the terminal-failure path. Persist each late
		// completion signal to the outbox; the server is the authority for
		// fail/complete races. We intentionally do not deduplicate on the
		// device: a second late signal gets its own fact_id and is also
		// delivered, because completed is an absorbing state on the server.
		sessionID, workDir := cs.sessionID, cs.workDir
		d.mu.Unlock()
		_ = d.writeCompleteFactToOutbox(taskID, sessionID, workDir, sig)
		return nil
	}
	if cs.set {
		d.mu.Unlock()
		return nil
	}
	// Durability first: persist the fact before closing notify and letting
	// execute report completion in-process. In-process completion proceeds even
	// if the outbox write fails.
	_ = d.writeCompleteFactToOutbox(taskID, cs.sessionID, cs.workDir, sig)
	cs.payload = sig
	cs.set = true
	close(cs.notify)
	d.mu.Unlock()
	return nil
}

// writeCompleteFactToOutbox persists a "complete" task fact durably. It returns
// nil when the fact is persisted (or when there is no outbox). Failures are
// logged loudly and returned so callers can decide whether to advance state.
func (d *Driver) writeCompleteFactToOutbox(taskID, sessionID, workDir string, sig agent.CompletionSignal) error {
	if d.outbox == nil {
		return nil
	}
	factID, err := newFactID()
	if err != nil {
		logger.Error("workflow: task %s failed to generate complete fact id: %v", taskID, err)
		return err
	}
	fact := OutboxFact{
		FactID:     factID,
		TaskID:     taskID,
		Kind:       "complete",
		OccurredAt: time.Now().UTC(),
		Output:     truncateOutput(sig.Summary),
		SessionID:  sessionID,
		WorkDir:    workDir,
		Decision:   sig.Decision,
		Reason:     sig.Reason,
	}
	if err := d.outbox.Add(fact); err != nil {
		logger.Error("workflow: task %s complete fact write-through failed: %v", taskID, err)
		return err
	}
	return nil
}
