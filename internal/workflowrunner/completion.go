package workflowrunner

import (
	"context"
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
	// Late mode: execute has entered a terminal-failure path, which never
	// consumes the registry (popCompletionSignal only runs on the success
	// path). A failure can be a misjudgment — the agent may still be alive
	// and finish its work — so signals arriving in late mode, and a signal
	// latched just before the mark, are forwarded to the server directly
	// instead of disappearing into the registry.
	late      bool
	forwarded bool
	sessionID string
	workDir   string
}

// registerCompletion creates the completion state for a task (csc only). It is
// called by execute before runAgent so the localserver handler can signal the
// agent's explicit completion while the session is still busy. sessionID and
// workDir are kept so a late completion can be forwarded with the same resume
// pointer the normal path would report.
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

// markCompletionLate switches the task's completion state to late mode: from
// here on, completion signals are forwarded to the server directly because
// execute's failure path will never pop them. A signal already latched is
// flushed immediately. No-op when the task has no completion state.
func (d *Driver) markCompletionLate(taskID string) {
	d.mu.Lock()
	cs, ok := d.completion[taskID]
	if !ok || cs.late {
		d.mu.Unlock()
		return
	}
	cs.late = true
	if cs.set && !cs.forwarded {
		cs.forwarded = true
		payload := cs.payload
		sessionID, workDir := cs.sessionID, cs.workDir
		d.mu.Unlock()
		go d.forwardLateCompletion(taskID, sessionID, workDir, payload)
		return
	}
	d.mu.Unlock()
}

// forwardLateCompletion reports a completion signal that arrived after execute
// entered the terminal-failure path. The server arbitrates the resulting race
// (fail-then-complete / complete-then-fail) — the driver's job is only to
// report honestly and log the outcome.
func (d *Driver) forwardLateCompletion(taskID, sessionID, workDir string, sig agent.CompletionSignal) {
	logger.Info("workflow: task %s forwarding late completion: action=%s decision=%s", taskID, sig.Action, sig.Decision)
	output := truncateOutput(sig.Summary)
	err := d.withTaskCallbackContext(func(ctx context.Context) error {
		return d.completeTaskOrFailOnRejection(ctx, taskID, output, sessionID, workDir, sig)
	})
	if err != nil {
		logger.Warn("workflow: task %s late completion forward failed: %v", taskID, err)
	}
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
// In late mode (execute is already failing the task) the signal is forwarded
// to the server directly, exactly once. Returns an error if the task is not
// running (already finished / unknown) so the handler can respond 409.
func (d *Driver) SignalTaskCompletion(taskID string, sig agent.CompletionSignal) error {
	d.mu.Lock()
	cs, ok := d.completion[taskID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("task %s is not running", taskID)
	}
	if cs.late {
		if cs.forwarded {
			d.mu.Unlock()
			return nil
		}
		cs.forwarded = true
		sessionID, workDir := cs.sessionID, cs.workDir
		d.mu.Unlock()
		// Durability first: persist the fact before any in-process delivery.
		d.writeCompleteFactToOutbox(taskID, sessionID, workDir, sig)
		go d.forwardLateCompletion(taskID, sessionID, workDir, sig)
		return nil
	}
	if cs.set {
		d.mu.Unlock()
		return nil
	}
	// Durability first: persist the fact before closing notify and letting
	// execute report completion in-process.
	d.writeCompleteFactToOutbox(taskID, cs.sessionID, cs.workDir, sig)
	cs.payload = sig
	cs.set = true
	close(cs.notify)
	d.mu.Unlock()
	return nil
}

// writeCompleteFactToOutbox persists a "complete" task fact durably. Failures
// are logged loudly but do not block the in-process completion path, which is
// the best-effort fallback when the outbox cannot be written.
func (d *Driver) writeCompleteFactToOutbox(taskID, sessionID, workDir string, sig agent.CompletionSignal) {
	if d.outbox == nil {
		return
	}
	factID, err := newFactID()
	if err != nil {
		logger.Error("workflow: task %s failed to generate complete fact id: %v", taskID, err)
		return
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
	}
}
