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
	"cs-cloud/internal/sessionevent"
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
// The watch runs under a fresh context bounded by the driver's AgentTimeout,
// NOT ctx: the HTTP request that triggers an adopt is short-lived (the gateway
// caps it near 30s) while the agent turn can run for minutes. ctx is therefore
// intentionally not used as the watch deadline; it is retained in the signature
// so callers can pass a request-scoped context and so future synchronous setup
// here can honour it.
//
// The session's events flow through the runtime EventBus, not a private SSE
// channel: AdoptUserTurn subscribes to the bus for this session, then asks the
// csc agent to re-emit its per-session SSE stream onto the bus. Both the bus
// subscription and the SSE emission are established synchronously before this
// function returns, so the caller can forward the prompt to csc without racing
// past the busy/idle events.
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

	if d.eventBus == nil {
		cancel()
		d.release(taskID, rec)
		slog.Warn("adopted turn: event bus not configured", "task_id", taskID)
		_ = d.failAdoptedTurn(taskID, "adopted turn: event bus not configured", "agent_error")
		return fmt.Errorf("adopted turn: event bus not configured")
	}

	// Subscribe to the bus BEFORE opening the SSE stream, so events the stream
	// emits immediately on connect are buffered for the watcher, not missed.
	events := d.eventBus.SubscribeSession(sessionID)

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
	emitErr := agent.EmitSessionEvents(establishCtx, sessionID)
	establishMu.Lock()
	establishDone = true
	establishMu.Unlock()
	if emitErr != nil {
		establishCancel()
		cancel()
		d.eventBus.Unsubscribe(events)
		d.release(taskID, rec)
		slog.Warn("adopted turn: subscribe failed", "task_id", taskID, "error", emitErr)
		_ = d.failAdoptedTurn(taskID, "adopted turn: event stream unavailable", "agent_error")
		return emitErr
	}

	go d.watchAdoptedTurn(watchCtx, cancel, taskID, sessionID, agent, rec, events)
	return nil
}

// watchAdoptedTurn consumes the session's events from the EventBus until the
// turn ends, forwards the final assistant message for the live transcript, then
// completes or fails the task. Always unsubscribes from the bus, releases the
// running-map slot, and cancels watchCtx (which propagates to the SSE stream).
func (d *Driver) watchAdoptedTurn(ctx context.Context, cancel context.CancelFunc, taskID, sessionID string, agent *csc.Agent, rec *taskRecord, events chan csagent.Event) {
	// defer order (LIFO): stop the SSE producer (cancel watchCtx → establishCtx)
	// first, then close the bus subscription, then free the running slot.
	defer d.release(taskID, rec)
	defer d.eventBus.Unsubscribe(events)
	defer cancel()

	waitErr := sessionevent.WaitForSessionDone(ctx, events)
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
	// Persist the completion durably before any external side-effect, mirroring
	// the dispatched path: a crash between the outcome and the in-process
	// CompleteTask callback must not lose the produced output. The fact carries
	// the output (truncated) so crash recovery can still surface it.
	_ = d.writeCompleteFactToOutbox(taskID, sessionID, workDir, csagent.CompletionSignal{Summary: output})
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
