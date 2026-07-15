package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

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
	d := workflowagent.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflowDriver(d))

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
	d := workflowagent.NewDriver(cfg, &workflowagent.Dependencies{
		MulticaBaseURL: "http://localhost:1",
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflowDriver(d))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/workflow/health", nil)
	rec := httptest.NewRecorder()
	s.handleWorkflowHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleWorkflowTaskRun(t *testing.T) {
	multica := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer multica.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"true"},
	}
	d := workflowagent.NewDriver(cfg, &workflowagent.Dependencies{
		MulticaBaseURL: multica.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflowDriver(d))

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
}

func TestHandleWorkflowTaskAbort(t *testing.T) {
	multica := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer multica.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"sh"},
	}
	d := workflowagent.NewDriver(cfg, &workflowagent.Dependencies{
		MulticaBaseURL: multica.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	defer d.Stop()

	s := New(WithWorkflowDriver(d))

	// Start a long-running task in the background.
	go func() {
		payload := workflow.TaskRunPayload{
			TaskID:      "task-1",
			WorkspaceID: "ws-1",
			Agent:       "sh",
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
