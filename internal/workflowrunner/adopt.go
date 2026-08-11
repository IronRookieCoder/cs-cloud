package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	csagent "cs-cloud/internal/agent"
	"cs-cloud/internal/agent/csc"
)

// adoptEstablishmentTimeout bounds only the SSE subscription setup phase in
// AdoptUserTurn. The full watch context still lasts for the configured
// AgentTimeout so the stream is not torn down prematurely.
const defaultAdoptEstablishmentTimeout = 15 * time.Second

// ErrTaskAlreadyRunning is returned by AdoptUserTurn when the task is already
// being executed or watched by this driver.
var ErrTaskAlreadyRunning = errors.New("task already running")

// AdoptUserTurn registers a user-initiated prompt turn on a session that is
// bound to workflow taskID, then watches the turn and reports the outcome to
// the multica backend exactly like a normal dispatch would. The prompt itself
// is NOT sent here — the proxy path already delivered it to csc.
//
// The SSE subscription is established synchronously before this function
// returns, so the caller can forward the prompt to csc without racing past the
// busy/idle events.
func (d *Driver) AdoptUserTurn(ctx context.Context, taskID, sessionID string, agent *csc.Agent) error {
	rec, err := d.reserveTaskID(taskID)
	if err != nil {
		if errors.Is(err, ErrTaskAlreadyRunning) {
			return ErrTaskAlreadyRunning
		}
		return err
	}

	watchCtx, cancel := context.WithTimeout(context.Background(), d.cfg.AgentTimeout)
	// Wire the cancel into the task record so AbortTask and Stop can end the
	// watch, exactly like a dispatched run.
	d.armCancel(rec, cancel)

	// Bound only the SSE subscription establishment; the long-lived watchCtx
	// remains valid for the full agent timeout once the stream is connected.
	// This prevents a csc that accepts TCP but never writes /event headers from
	// stalling the proxy handler for the entire AgentTimeout.
	establishCtx, establishCancel := context.WithCancel(watchCtx)
	var establishMu sync.Mutex
	establishDone := false
	go func() {
		select {
		case <-time.After(d.adoptEstablishmentTimeout):
			establishMu.Lock()
			if !establishDone {
				establishCancel()
			}
			establishMu.Unlock()
		}
	}()
	events, err := agent.SubscribeSessionEvents(establishCtx, sessionID)
	establishMu.Lock()
	establishDone = true
	establishMu.Unlock()
	if err != nil {
		establishCancel()
		cancel()
		d.release(taskID, rec)
		slog.Warn("adopted turn: subscribe failed", "task_id", taskID, "error", err)
		_ = d.failAdoptedTurn(taskID, "adopted turn: event stream unavailable", "agent_error")
		return err
	}

	go d.watchAdoptedTurn(watchCtx, cancel, taskID, sessionID, agent, rec, events)
	return nil
}

// watchAdoptedTurn consumes the session's event stream until the turn ends,
// forwards the final assistant message for the live transcript, then completes
// or fails the task. Always releases the running-map slot and cancels watchCtx.
func (d *Driver) watchAdoptedTurn(ctx context.Context, cancel context.CancelFunc, taskID, sessionID string, agent *csc.Agent, rec *taskRecord, events <-chan csc.SessionEvent) {
	defer cancel()
	defer d.release(taskID, rec)

	waitErr := csc.WaitForSessionDone(ctx, events)
	if waitErr != nil {
		if d.aborted(taskID) {
			// Mirror the dispatched-run path: a user abort is reported as
			// cancelled, not as an agent failure.
			_ = d.failAdoptedTurn(taskID, fmt.Sprintf("aborted: %v", waitErr), "cancelled")
			return
		}
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
	// Persist the failure durably before the in-process callback, mirroring the
	// dispatched path: a crash between the outcome and FailTask must not lose
	// the signal. Best-effort like the dispatched path; delivery is at-least-once.
	d.writeFailFactToOutbox(taskID, errors.New(reason), failureReason)
	return d.withTaskCallbackContext(func(callbackCtx context.Context) error {
		return d.client.FailTask(callbackCtx, taskID, reason, failureReason)
	})
}
