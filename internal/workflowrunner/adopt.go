package workflowrunner

import (
	"context"
	"errors"
	"log/slog"

	csagent "cs-cloud/internal/agent"
	"cs-cloud/internal/agent/csc"
)

// ErrTaskAlreadyRunning is returned by AdoptUserTurn when the task is already
// being executed or watched by this driver.
var ErrTaskAlreadyRunning = errors.New("task already running")

// AdoptUserTurn registers a user-initiated prompt turn on a session that is
// bound to workflow taskID, then watches the turn and reports the outcome to
// the multica backend exactly like a normal dispatch would. The prompt itself
// is NOT sent here — the proxy path already delivered it to csc.
func (d *Driver) AdoptUserTurn(ctx context.Context, taskID, sessionID string, agent *csc.Agent) error {
	rec, err := d.reserveTaskID(taskID)
	if err != nil {
		return ErrTaskAlreadyRunning
	}
	go d.watchAdoptedTurn(taskID, sessionID, agent, rec)
	return nil
}

// watchAdoptedTurn consumes the session's event stream until the turn ends,
// forwards the final assistant message for the live transcript, then completes
// or fails the task. Always releases the running-map slot.
func (d *Driver) watchAdoptedTurn(taskID, sessionID string, agent *csc.Agent, rec *taskRecord) {
	defer d.release(taskID, rec)

	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.AgentTimeout)
	defer cancel()

	events, err := agent.SubscribeSessionEvents(ctx, sessionID)
	if err != nil {
		slog.Warn("adopted turn: subscribe failed", "task_id", taskID, "error", err)
		_ = d.failAdoptedTurn(taskID, "adopted turn: event stream unavailable", "agent_error")
		return
	}

	waitErr := csc.WaitForSessionDone(ctx, events)
	if waitErr != nil {
		if errors.Is(waitErr, context.DeadlineExceeded) {
			_ = d.failAdoptedTurn(taskID, "adopted turn timed out", "agent_timeout")
			return
		}
		failureReason := "agent_error"
		if errors.Is(waitErr, csagent.ErrIncomplete) {
			failureReason = "agent_incomplete"
		}
		_ = d.failAdoptedTurn(taskID, waitErr.Error(), failureReason)
		return
	}

	output, err := d.lastAssistantText(ctx, agent, sessionID)
	if err != nil {
		_ = d.failAdoptedTurn(taskID, err.Error(), "agent_error")
		return
	}
	if output == "" {
		_ = d.failAdoptedTurn(taskID, "adopted turn completed without assistant output", "agent_empty_output")
		return
	}

	workDir, _ := agent.SessionDirectory(ctx, sessionID)
	d.postTaskMessages(taskID, output)
	_ = d.withTaskCallbackContext(func(callbackCtx context.Context) error {
		return d.client.CompleteTask(callbackCtx, taskID, output, sessionID, workDir, csagent.CompletionSignal{})
	})
}

// lastAssistantText fetches the session message list and returns the most
// recent non-empty assistant text.
func (d *Driver) lastAssistantText(ctx context.Context, agent *csc.Agent, sessionID string) (string, error) {
	body, err := agent.GetSessionMessages(ctx, sessionID)
	if err != nil {
		return "", err
	}
	return csc.ExtractLastAssistantText(body)
}

// failAdoptedTurn reports a terminal failure for an adopted turn.
func (d *Driver) failAdoptedTurn(taskID, reason, failureReason string) error {
	slog.Warn("adopted turn failed", "task_id", taskID, "failure_reason", failureReason, "reason", reason)
	return d.withTaskCallbackContext(func(callbackCtx context.Context) error {
		return d.client.FailTask(callbackCtx, taskID, reason, failureReason)
	})
}
