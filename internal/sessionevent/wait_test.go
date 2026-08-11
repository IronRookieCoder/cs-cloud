package sessionevent

import (
	"context"
	"testing"
	"time"

	"cs-cloud/internal/agent"
)

func TestWaitForSessionDoneWaitsForBusyThenIdle(t *testing.T) {
	events := make(chan agent.Event, 4)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	if err := WaitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("WaitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneIgnoresIdleBeforeBusy(t *testing.T) {
	events := make(chan agent.Event, 4)
	// A stale idle event (e.g. emitted before our prompt started) must not
	// end the wait.
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "idle"},
	}}
	close(events)

	if err := WaitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("WaitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneReturnsTerminalSessionError(t *testing.T) {
	events := make(chan agent.Event, 4)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "error_max_turns",
		"isError": true,
	}}
	events <- agent.Event{Type: "session.error", Data: map[string]any{
		"error": map[string]any{
			"subtype": "error_max_turns",
			"message": "Max turns reached",
		},
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	err := WaitForSessionDone(context.Background(), events)
	if err == nil {
		t.Fatal("expected terminal session error, got nil")
	}
	if got := err.Error(); got != "Max turns reached" {
		t.Fatalf("error = %q, want %q", got, "Max turns reached")
	}
}

func TestWaitForSessionDoneAllowsAPIRetryToRecover(t *testing.T) {
	events := make(chan agent.Event, 5)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.error", Data: map[string]any{
		"error": map[string]any{
			"subtype": "api_retry",
			"message": "rate limit",
		},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "success",
		"isError": false,
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	if err := WaitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("WaitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneTreatsSnakeCaseErrorFlagAsFailure(t *testing.T) {
	events := make(chan agent.Event, 3)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype":  "success",
		"is_error": true,
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	if err := WaitForSessionDone(context.Background(), events); err == nil {
		t.Fatal("expected result with is_error=true to fail")
	}
}

func TestWaitForSessionDoneTreatsUnknownNonSuccessSubtypeAsFailure(t *testing.T) {
	events := make(chan agent.Event, 3)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "future_terminal_error",
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	err := WaitForSessionDone(context.Background(), events)
	if err == nil || err.Error() != "csc session failed: future_terminal_error" {
		t.Fatalf("error = %v, want unknown subtype failure", err)
	}
}

func TestWaitForSessionDoneAllowsToolErrorWhenSessionSucceeds(t *testing.T) {
	events := make(chan agent.Event, 4)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "message.part.updated", Data: map[string]any{
		"part": map[string]any{
			"type":     "tool",
			"is_error": true,
			"output":   "command failed",
		},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "success",
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	if err := WaitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("WaitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneIgnoresTerminalEventsBeforeBusy(t *testing.T) {
	events := make(chan agent.Event, 6)
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "stale_failure",
		"isError": true,
	}}
	events <- agent.Event{Type: "session.error", Data: map[string]any{
		"error": map[string]any{
			"subtype": "stale_failure",
			"message": "stale failure message",
		},
	}}
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "current_failure",
		"isError": true,
		"errors": []any{
			"current failure message",
		},
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	err := WaitForSessionDone(context.Background(), events)
	if err == nil || err.Error() != "current failure message" {
		t.Fatalf("error = %v, want current failure message", err)
	}
}

func TestWaitForSessionDoneUsesResultErrorWithoutSessionErrorEvent(t *testing.T) {
	events := make(chan agent.Event, 3)
	events <- agent.Event{Type: "session.status", Data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- agent.Event{Type: "session.result", Data: map[string]any{
		"subtype": "error_max_turns",
		"isError": true,
		"errors": []any{
			"Max turns reached",
			"second error",
		},
	}}
	events <- agent.Event{Type: "session.idle", Data: map[string]any{}}
	close(events)

	err := WaitForSessionDone(context.Background(), events)
	if err == nil || err.Error() != "Max turns reached" {
		t.Fatalf("error = %v, want first result error", err)
	}
}

func TestWaitForSessionDoneCancelled(t *testing.T) {
	events := make(chan agent.Event)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := WaitForSessionDone(ctx, events); err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestWaitForSessionDoneStreamClosedEarly(t *testing.T) {
	events := make(chan agent.Event)
	close(events)

	if err := WaitForSessionDone(context.Background(), events); err == nil {
		t.Fatal("expected stream-closed error, got nil")
	}
}
