package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/config"
	"cs-cloud/internal/platform"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner"
)

func installWorkflowHandlerFakeAgent(t *testing.T, name string) {
	t.Helper()

	dir := t.TempDir()
	var bin string
	if runtime.GOOS == "windows" {
		bin = filepath.Join(dir, name+".cmd")
		script := `@echo off
set first=%~1
if /I "%first:~0,6%"=="sleep " (
  :wait
  goto wait
  exit /b 0
)
echo %*
`
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake agent: %v", err)
		}
	} else {
		bin = filepath.Join(dir, name)
		script := `#!/bin/sh
case "$1" in
  "sleep "*) exec sleep "${1#sleep }" ;;
  *) printf '%s\n' "$@" ;;
esac
`
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatalf("write fake agent: %v", err)
		}
	}

	path := os.Getenv("PATH")
	if path != "" {
		path = string(os.PathListSeparator) + path
	}
	t.Setenv("PATH", dir+path)
}

func TestHandleWorkflowHealthNotRegistered(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/health", nil)
	rec := httptest.NewRecorder()
	s.handleWorkflowHealth(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowHealthNotStarted(t *testing.T) {
	d := workflowrunner.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflow(d))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/health", nil)
	rec := httptest.NewRecorder()
	s.handleWorkflowHealth(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowHealthRunning(t *testing.T) {
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: "http://localhost:1",
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflow(d))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/health", nil)
	rec := httptest.NewRecorder()
	s.handleWorkflowHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskRun(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"true"},
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflow(d))

	payload := workflow.TaskRunPayload{
		TaskID:      "task-1",
		WorkspaceID: "ws-1",
		Agent:       "true",
		Prompt:      "hello",
	}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-1/run", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskRun(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "accepted") {
		t.Fatalf("body = %s, want status accepted", rec.Body.String())
	}
}

func TestHandleWorkflowTaskRunMissingTaskID(t *testing.T) {
	d := workflowrunner.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflow(d))

	payload := workflow.TaskRunPayload{WorkspaceID: "ws-1", Agent: "true", Prompt: "hello"}
	b, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/x/run", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskRun(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskRunDuplicate(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"sh"},
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflow(d))

	payload := workflow.TaskRunPayload{
		TaskID:      "task-dup",
		WorkspaceID: "ws-1",
		Agent:       "sh",
		Prompt:      "sleep 5",
	}
	b, _ := json.Marshal(payload)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-dup/run", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskRun(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first run: code = %d, body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-dup/run", bytes.NewReader(b))
	rec = httptest.NewRecorder()
	s.handleWorkflowTaskRun(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate run: code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskCompleteNotRegistered(t *testing.T) {
	s := New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-1/complete", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("id", "task-1")
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskComplete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskCompleteNotRunning(t *testing.T) {
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: "http://localhost:1",
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()
	s := New(WithWorkflow(d))

	body := []byte(`{"action":"complete","summary":"done"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-not-running/complete", bytes.NewReader(body))
	req.SetPathValue("id", "task-not-running")
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskComplete(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("code = %d, body = %s; want 409 (task not running)", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskCompleteBadBody(t *testing.T) {
	d := workflowrunner.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflow(d))
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-1/complete", strings.NewReader("not-json"))
	req.SetPathValue("id", "task-1")
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskComplete(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskAbort(t *testing.T) {
	installWorkflowHandlerFakeAgent(t, "fakeagent")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"fakeagent"},
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflow(d))

	// Start a long-running task in the background.
	go func() {
		payload := workflow.TaskRunPayload{
			TaskID:      "task-1",
			WorkspaceID: "ws-1",
			Agent:       "fakeagent",
			Prompt:      "sleep 10",
		}
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-1/run", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		s.handleWorkflowTaskRun(rec, req)
	}()

	time.Sleep(100 * time.Millisecond)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-1/abort", nil)
	req.SetPathValue("id", "task-1")
	rec := httptest.NewRecorder()
	s.handleWorkflowTaskAbort(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskFacts(t *testing.T) {
	installWorkflowHandlerFakeAgent(t, "fakeagent")

	oldDataDir := platform.DataDir()
	defer platform.SetDataDir(oldDataDir)
	platform.SetDataDir(t.TempDir())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"fakeagent"},
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	if err := d.Outbox().Add(workflowrunner.OutboxFact{
		FactID:     "fact-a-pending",
		TaskID:     "task-a",
		Kind:       "complete",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		Output:     "pending output",
		SessionID:  "session-a",
		WorkDir:    "/tmp/a",
	}); err != nil {
		t.Fatalf("add pending fact: %v", err)
	}
	if err := d.Outbox().Add(workflowrunner.OutboxFact{
		FactID:     "fact-a-done",
		TaskID:     "task-a",
		Kind:       "complete",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 1, 0, time.UTC),
		Output:     "done output",
		SessionID:  "session-a",
		WorkDir:    "/tmp/a",
	}); err != nil {
		t.Fatalf("add done fact: %v", err)
	}
	if err := d.Outbox().MarkDone("fact-a-done"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	if err := d.Outbox().Add(workflowrunner.OutboxFact{
		FactID:     "fact-b",
		TaskID:     "task-b",
		Kind:       "fail",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 2, 0, time.UTC),
		Error:      "b failed",
		SessionID:  "session-b",
		WorkDir:    "/tmp/b",
	}); err != nil {
		t.Fatalf("add task-b fact: %v", err)
	}

	// Start task-a so it is in the running table at query time.
	go func() {
		payload := workflow.TaskRunPayload{
			TaskID:      "task-a",
			WorkspaceID: "ws-a",
			Agent:       "fakeagent",
			Prompt:      "sleep 10",
		}
		b, _ := json.Marshal(payload)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/tasks/task-a/run", bytes.NewReader(b))
		rec := httptest.NewRecorder()
		s := New(WithWorkflow(d))
		s.handleWorkflowTaskRun(rec, req)
	}()

	time.Sleep(100 * time.Millisecond)

	s := New(WithWorkflow(d))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/facts?task_id=task-a", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Facts       []workflowrunner.OutboxFact `json:"facts"`
		TaskRunning bool                        `json:"task_running"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v (body=%s)", err, rec.Body.String())
	}
	if len(resp.Facts) != 2 {
		t.Fatalf("expected 2 facts for task-a, got %d", len(resp.Facts))
	}
	if !sort.SliceIsSorted(resp.Facts, func(i, j int) bool {
		if !resp.Facts[i].OccurredAt.Equal(resp.Facts[j].OccurredAt) {
			return resp.Facts[i].OccurredAt.Before(resp.Facts[j].OccurredAt)
		}
		return resp.Facts[i].FactID < resp.Facts[j].FactID
	}) {
		t.Fatalf("facts not sorted: %+v", resp.Facts)
	}
	for _, f := range resp.Facts {
		if f.TaskID != "task-a" {
			t.Fatalf("fact leaked from another task: %+v", f)
		}
	}
	if !resp.TaskRunning {
		t.Fatalf("expected task-a to be reported as running")
	}
}

func TestHandleWorkflowTaskFacts_NotRunning(t *testing.T) {
	oldDataDir := platform.DataDir()
	defer platform.SetDataDir(oldDataDir)
	platform.SetDataDir(t.TempDir())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	if err := d.Outbox().Add(workflowrunner.OutboxFact{
		FactID:     "fact-a",
		TaskID:     "task-a",
		Kind:       "complete",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		Output:     "done",
	}); err != nil {
		t.Fatalf("add fact: %v", err)
	}

	s := New(WithWorkflow(d))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/facts?task_id=task-a", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Facts       []workflowrunner.OutboxFact `json:"facts"`
		TaskRunning bool                        `json:"task_running"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if len(resp.Facts) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(resp.Facts))
	}
	if resp.TaskRunning {
		t.Fatalf("expected task_running=false for a non-running task")
	}
}

func TestHandleWorkflowTaskFacts_MissingTaskID(t *testing.T) {
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
	}
	d := workflowrunner.NewDriver(cfg, nil)
	s := New(WithWorkflow(d))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/facts", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, body = %s; want 400", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskFacts_UnknownTaskID(t *testing.T) {
	oldDataDir := platform.DataDir()
	defer platform.SetDataDir(oldDataDir)
	platform.SetDataDir(t.TempDir())

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflow(d))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/facts?task_id=unknown", nil)
	rec := httptest.NewRecorder()
	s.http.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Facts       []workflowrunner.OutboxFact `json:"facts"`
		TaskRunning bool                        `json:"task_running"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Facts == nil || len(resp.Facts) != 0 {
		t.Fatalf("expected empty non-nil facts, got %v", resp.Facts)
	}
	if resp.TaskRunning {
		t.Fatalf("expected task_running=false for unknown task")
	}
}

func TestHandleWorkflowTaskFacts_Auth(t *testing.T) {
	const key = "workflow-facts-key"
	srv := New(WithVersion("test"), WithConfig(&config.Config{APIKey: key}))

	t.Run("rejects without key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/facts?task_id=task-1", nil)
		srv.http.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("expected 401, got %d (body=%s)", rec.Code, rec.Body.String())
		}
	})

	t.Run("accepts with x-api-key", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/facts?task_id=task-1", nil)
		req.Header.Set("X-API-Key", key)
		srv.http.Handler.ServeHTTP(rec, req)
		// No workflow driver registered -> the auth'd request reaches the handler.
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404 (driver not registered), got %d (body=%s)", rec.Code, rec.Body.String())
		}
	})
}
