package csc

import (
	"context"
	"testing"
	"time"
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
