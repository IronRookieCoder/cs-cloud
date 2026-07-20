package localserver

import (
	"context"
	"io"
	"net/http"
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
