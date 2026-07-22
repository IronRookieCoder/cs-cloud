package localserver

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

func TestWorkflowHealthRoute(t *testing.T) {
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

	s := New(WithWorkflow(d))
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer s.Shutdown(context.Background())

	resp, err := http.Get(s.URL() + "/api/v1/workflow/health")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, body)
	}
}

// A daemon registered against a server without the workflow backend has no
// multica base URL, so the driver cannot start. The server must come up
// anyway (the workflow subsystem is optional), and the workflow endpoints
// must report 503 with the underlying reason instead of a dead daemon.
func TestServerStartDegradesWhenWorkflowDriverFails(t *testing.T) {
	d := workflowagent.NewDriver(workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
	}, &workflowagent.Dependencies{})

	s := New(WithWorkflow(d))
	if err := s.Start("127.0.0.1:0"); err != nil {
		t.Fatalf("start server: %v", err)
	}
	defer s.Shutdown(context.Background())

	if s.workflowErr == nil {
		t.Fatal("expected workflowErr to record the driver start failure")
	}

	resp, err := http.Get(s.URL() + "/api/v1/workflow/health")
	if err != nil {
		t.Fatalf("health request: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("health status = %d, body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "multica base URL") {
		t.Fatalf("health body should name the root cause, got %s", body)
	}

	runResp, err := http.Post(s.URL()+"/api/v1/workflow/tasks/task-1/run", "application/json",
		strings.NewReader(`{"task_id":"task-1","workspace_id":"ws-1","agent":"true","prompt":"hi"}`))
	if err != nil {
		t.Fatalf("run request: %v", err)
	}
	runBody, _ := io.ReadAll(runResp.Body)
	runResp.Body.Close()
	if runResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("run status = %d, body = %s", runResp.StatusCode, runBody)
	}
}
