package csc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/agent"
)

func TestWaitForSessionDoneWaitsForBusyThenIdle(t *testing.T) {
	events := make(chan sessionEvent, 4)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	if err := waitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("waitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneIgnoresIdleBeforeBusy(t *testing.T) {
	events := make(chan sessionEvent, 4)
	// A stale idle event (e.g. emitted before our prompt started) must not
	// end the wait.
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "idle"},
	}}
	close(events)

	if err := waitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("waitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneReturnsTerminalSessionError(t *testing.T) {
	events := make(chan sessionEvent, 4)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "error_max_turns",
		"isError": true,
	}}
	events <- sessionEvent{name: "session.error", data: map[string]any{
		"error": map[string]any{
			"subtype": "error_max_turns",
			"message": "Max turns reached",
		},
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	err := waitForSessionDone(context.Background(), events)
	if err == nil {
		t.Fatal("expected terminal session error, got nil")
	}
	if got := err.Error(); got != "Max turns reached" {
		t.Fatalf("error = %q, want %q", got, "Max turns reached")
	}
}

func TestWaitForSessionDoneAllowsAPIRetryToRecover(t *testing.T) {
	events := make(chan sessionEvent, 5)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.error", data: map[string]any{
		"error": map[string]any{
			"subtype": "api_retry",
			"message": "rate limit",
		},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "success",
		"isError": false,
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	if err := waitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("waitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneTreatsSnakeCaseErrorFlagAsFailure(t *testing.T) {
	events := make(chan sessionEvent, 3)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype":  "success",
		"is_error": true,
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	if err := waitForSessionDone(context.Background(), events); err == nil {
		t.Fatal("expected result with is_error=true to fail")
	}
}

func TestWaitForSessionDoneTreatsUnknownNonSuccessSubtypeAsFailure(t *testing.T) {
	events := make(chan sessionEvent, 3)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "future_terminal_error",
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	err := waitForSessionDone(context.Background(), events)
	if err == nil || err.Error() != "csc session failed: future_terminal_error" {
		t.Fatalf("error = %v, want unknown subtype failure", err)
	}
}

func TestWaitForSessionDoneAllowsToolErrorWhenSessionSucceeds(t *testing.T) {
	events := make(chan sessionEvent, 4)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "message.part.updated", data: map[string]any{
		"part": map[string]any{
			"type":     "tool",
			"is_error": true,
			"output":   "command failed",
		},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "success",
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	if err := waitForSessionDone(context.Background(), events); err != nil {
		t.Fatalf("waitForSessionDone: %v", err)
	}
}

func TestWaitForSessionDoneIgnoresTerminalEventsBeforeBusy(t *testing.T) {
	events := make(chan sessionEvent, 6)
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "stale_failure",
		"isError": true,
	}}
	events <- sessionEvent{name: "session.error", data: map[string]any{
		"error": map[string]any{
			"subtype": "stale_failure",
			"message": "stale failure message",
		},
	}}
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "current_failure",
		"isError": true,
		"errors": []any{
			"current failure message",
		},
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	err := waitForSessionDone(context.Background(), events)
	if err == nil || err.Error() != "current failure message" {
		t.Fatalf("error = %v, want current failure message", err)
	}
}

func TestWaitForSessionDoneUsesResultErrorWithoutSessionErrorEvent(t *testing.T) {
	events := make(chan sessionEvent, 3)
	events <- sessionEvent{name: "session.status", data: map[string]any{
		"status": map[string]any{"type": "busy"},
	}}
	events <- sessionEvent{name: "session.result", data: map[string]any{
		"subtype": "error_max_turns",
		"isError": true,
		"errors": []any{
			"Max turns reached",
			"second error",
		},
	}}
	events <- sessionEvent{name: "session.idle", data: map[string]any{}}
	close(events)

	err := waitForSessionDone(context.Background(), events)
	if err == nil || err.Error() != "Max turns reached" {
		t.Fatalf("error = %v, want first result error", err)
	}
}

func TestWaitForSessionDoneCancelled(t *testing.T) {
	events := make(chan sessionEvent)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := waitForSessionDone(ctx, events); err == nil {
		t.Fatal("expected context error, got nil")
	}
}

func TestWaitForSessionDoneStreamClosedEarly(t *testing.T) {
	events := make(chan sessionEvent)
	close(events)

	if err := waitForSessionDone(context.Background(), events); err == nil {
		t.Fatal("expected stream-closed error, got nil")
	}
}

func TestRunSessionReturnsTerminalErrorFromEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"error_max_turns\",\"isError\":true}\n\n"))
			_, _ = w.Write([]byte("event: session.error\n"))
			_, _ = w.Write([]byte("data: {\"error\":{\"subtype\":\"error_max_turns\",\"message\":\"Max turns reached\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/prompt_async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	_, err := agent.RunSession(context.Background(), "session-1", t.TempDir(), "do thing", nil)
	if err == nil {
		t.Fatal("expected terminal session error, got nil")
	}
	if got, want := err.Error(), "wait for completion: Max turns reached"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestRunSessionRejectsCompletedSessionWithoutAssistantOutput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("event: session.status\n"))
			_, _ = w.Write([]byte("data: {\"status\":{\"type\":\"busy\"}}\n\n"))
			_, _ = w.Write([]byte("event: session.result\n"))
			_, _ = w.Write([]byte("data: {\"subtype\":\"success\"}\n\n"))
			_, _ = w.Write([]byte("event: session.idle\n"))
			_, _ = w.Write([]byte("data: {}\n\n"))
		case r.Method == http.MethodPost && r.URL.Path == "/session/session-1/prompt_async":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1/message":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"messages":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cscAgent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	_, err := cscAgent.RunSession(context.Background(), "session-1", t.TempDir(), "do thing", nil)
	if !errors.Is(err, agent.ErrEmptySessionOutput) {
		t.Fatalf("RunSession error = %v, want ErrEmptySessionOutput", err)
	}
}

func TestCreateSessionFailsWhenWorkerStopsBeforeReady(t *testing.T) {
	var created bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/session-1":
			if !created {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"session-1","status":"stopped"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			created = true
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"session_id":"session-1","status":"starting"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	agent := &Agent{
		endpoint:    server.URL,
		rawEndpoint: server.URL,
		httpClient:  server.Client(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	err := agent.CreateSession(ctx, "session-1", t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "stopped before becoming ready") {
		t.Fatalf("CreateSession error = %v, want stopped-before-ready error", err)
	}
}

func TestExtractLastAssistantText(t *testing.T) {
	body := []byte(`{"messages":[
		{"role":"user","parts":[{"type":"text","text":"do it"}]},
		{"role":"assistant","parts":[
			{"type":"reasoning","text":"thinking"},
			{"type":"text","text":"first line"},
			{"type":"text","text":"second line"}
		]}
	]}`)

	out, err := extractLastAssistantText(body)
	if err != nil {
		t.Fatalf("extractLastAssistantText: %v", err)
	}
	if out != "first line\nsecond line" {
		t.Fatalf("out = %q", out)
	}
}

func TestEnvSliceToMap(t *testing.T) {
	got := envSliceToMap([]string{
		"MULTICA_NODE_RUN_ID=old",
		"INVALID",
		"=empty-key",
		"MULTICA_NODE_RUN_ID=nr-1",
		"CS_CLOUD_WORKTREE=C:\\work=tree",
	})

	if got["MULTICA_NODE_RUN_ID"] != "nr-1" {
		t.Fatalf("MULTICA_NODE_RUN_ID = %q, want nr-1", got["MULTICA_NODE_RUN_ID"])
	}
	if got["CS_CLOUD_WORKTREE"] != `C:\work=tree` {
		t.Fatalf("CS_CLOUD_WORKTREE = %q", got["CS_CLOUD_WORKTREE"])
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty key should be skipped")
	}
}
