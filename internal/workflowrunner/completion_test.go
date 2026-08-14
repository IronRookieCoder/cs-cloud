package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

func testAppDir(t *testing.T) string {
	t.Helper()
	prev := platform.DataDir()
	dir := t.TempDir()
	platform.SetDataDir(dir)
	t.Cleanup(func() { platform.SetDataDir(prev) })
	return platform.AppDir()
}

func TestSignalTaskCompletion_WritesCompleteFactToOutbox(t *testing.T) {
	appDir := testAppDir(t)
	cfg := workflow.Config{
		WorkspacesRoot: filepath.Join(appDir, "workflow", "workspaces"),
		CacheDir:       filepath.Join(appDir, "workflow", "cache"),
	}
	d := NewDriver(cfg, nil)
	d.outbox = NewOutbox(appDir)

	d.registerCompletion("task-1", "sess-1", "/tmp/wd")
	sig := agent.CompletionSignal{
		Action:   "complete",
		Summary:  "done summary",
		Decision: "approve",
		Reason:   "lgtm",
	}
	if err := d.SignalTaskCompletion("task-1", sig); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	pending, err := d.outbox.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact, got %d", len(pending))
	}
	f := pending[0]
	if f.Kind != "complete" {
		t.Errorf("kind = %q, want complete", f.Kind)
	}
	if f.TaskID != "task-1" {
		t.Errorf("task_id = %q, want task-1", f.TaskID)
	}
	if f.Output != "done summary" {
		t.Errorf("output = %q, want %q", f.Output, "done summary")
	}
	if f.SessionID != "sess-1" {
		t.Errorf("session_id = %q, want sess-1", f.SessionID)
	}
	if f.WorkDir != "/tmp/wd" {
		t.Errorf("work_dir = %q, want /tmp/wd", f.WorkDir)
	}
	if f.Decision != "approve" {
		t.Errorf("decision = %q, want approve", f.Decision)
	}
	if f.Reason != "lgtm" {
		t.Errorf("reason = %q, want lgtm", f.Reason)
	}
	if f.FactID == "" {
		t.Error("fact_id is empty")
	}
	if f.OccurredAt.IsZero() {
		t.Error("occurred_at is zero")
	}
	if time.Since(f.OccurredAt) > time.Second {
		t.Errorf("occurred_at too old: %v", f.OccurredAt)
	}
}

func TestFailTask_WritesFailFactToOutbox(t *testing.T) {
	appDir := testAppDir(t)
	cfg := workflow.Config{
		WorkspacesRoot: filepath.Join(appDir, "workflow", "workspaces"),
		CacheDir:       filepath.Join(appDir, "workflow", "cache"),
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	d := NewDriver(cfg, nil)
	d.outbox = NewOutbox(appDir)
	d.client = NewClient(backend.URL, "", func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: "x"}, nil
	})

	taskErr := errors.New("agent exploded")
	_ = d.failTask("task-1", taskErr, "agent_error")

	pending, err := d.outbox.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact, got %d", len(pending))
	}
	f := pending[0]
	if f.Kind != "fail" {
		t.Errorf("kind = %q, want fail", f.Kind)
	}
	if f.TaskID != "task-1" {
		t.Errorf("task_id = %q, want task-1", f.TaskID)
	}
	if f.Error != taskErr.Error() {
		t.Errorf("error = %q, want %q", f.Error, taskErr.Error())
	}
	if f.FailureReason != "agent_error" {
		t.Errorf("failure_reason = %q, want agent_error", f.FailureReason)
	}
	if f.FactID == "" {
		t.Error("fact_id is empty")
	}
	if f.OccurredAt.IsZero() {
		t.Error("occurred_at is zero")
	}
}

func TestSignalTaskCompletion_DeliversAfterRestart(t *testing.T) {
	appDir := testAppDir(t)

	// Phase 1: a running driver whose backend is down. The completion signal
	// must still be durably written to the outbox.
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer dead.Close()

	cfg := workflow.Config{
		WorkspacesRoot: filepath.Join(appDir, "workflow", "workspaces"),
		CacheDir:       filepath.Join(appDir, "workflow", "cache"),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
	}
	d1 := NewDriver(cfg, &Dependencies{
		BackendBaseURL: dead.URL,
		TokenProvider: func() (*provider.Credentials, error) {
			return &provider.Credentials{AccessToken: "x"}, nil
		},
	})
	if err := d1.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	d1.registerCompletion("task-restart", "sess-restart", "/tmp/wd")
	sig := agent.CompletionSignal{Action: "complete", Summary: "survived restart"}
	if err := d1.SignalTaskCompletion("task-restart", sig); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}
	if err := d1.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Phase 2: simulate daemon restart with a fresh outbox/driver pointing at a
	// live server. The pending fact from phase 1 must be delivered.
	var gotBody string
	called := make(chan struct{}, 1)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wantPath := fmt.Sprintf(workflow.TaskFactsEndpoint, "task-restart")
		if r.URL.Path != wantPath {
			t.Errorf("path = %q, want %q", r.URL.Path, wantPath)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		called <- struct{}{}
	}))
	defer live.Close()

	o2 := NewOutbox(appDir)
	pending, err := o2.Pending()
	if err != nil {
		t.Fatalf("Pending after restart: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact after restart, got %d", len(pending))
	}
	if pending[0].TaskID != "task-restart" {
		t.Errorf("pending task_id = %q, want task-restart", pending[0].TaskID)
	}

	d2 := &Driver{
		client: NewClient(live.URL, "", func() (*provider.Credentials, error) {
			return &provider.Credentials{AccessToken: "x"}, nil
		}),
		outbox: o2,
	}
	d2.deliverOutboxPass(context.Background())

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("fact was not delivered after restart")
	}

	pending, err = o2.Pending()
	if err != nil {
		t.Fatalf("Pending after delivery: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending facts after delivery, got %d", len(pending))
	}
	if !strings.Contains(gotBody, `"kind":"complete"`) {
		t.Errorf("delivered body missing complete kind: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"task_id":"task-restart"`) {
		t.Errorf("delivered body missing task_id: %s", gotBody)
	}
}
