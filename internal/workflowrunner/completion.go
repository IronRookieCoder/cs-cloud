package workflowrunner

import (
	"fmt"

	"cs-cloud/internal/agent"
)

// completionState tracks an explicit "complete task" signal for a running csc
// task. The agent invokes the complete tool via the localserver handler, which
// calls SignalTaskCompletion; that stores the payload and closes notify so
// runAgent can stop waiting on the session and report completion.
type completionState struct {
	notify  chan struct{}
	payload agent.CompletionSignal
	set     bool
}

// registerCompletion creates the completion state for a task (csc only). It is
// called by execute before runAgent so the localserver handler can signal the
// agent's explicit completion while the session is still busy.
func (d *Driver) registerCompletion(taskID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completion == nil {
		d.completion = make(map[string]*completionState)
	}
	d.completion[taskID] = &completionState{notify: make(chan struct{})}
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
// Returns an error if the task is not running (already finished / unknown) so
// the handler can respond 409.
func (d *Driver) SignalTaskCompletion(taskID string, sig agent.CompletionSignal) error {
	d.mu.Lock()
	cs, ok := d.completion[taskID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("task %s is not running", taskID)
	}
	if cs.set {
		d.mu.Unlock()
		return nil
	}
	cs.payload = sig
	cs.set = true
	close(cs.notify)
	d.mu.Unlock()
	return nil
}
