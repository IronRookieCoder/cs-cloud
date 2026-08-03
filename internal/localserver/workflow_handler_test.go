package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
