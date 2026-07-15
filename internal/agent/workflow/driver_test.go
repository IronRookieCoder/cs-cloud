package workflow

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/workflow"
)

func TestDriverImplementsPersistentDriver(t *testing.T) {
	var _ runtime.PersistentDriver = (*Driver)(nil)
}

func TestDriverName(t *testing.T) {
	deps := &Dependencies{
		MulticaBaseURL: "https://multica.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(workflow.DefaultConfig(), deps)
	if got, want := d.Name(), "workflow"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestDriverLifecycleNoop(t *testing.T) {
	deps := &Dependencies{
		MulticaBaseURL: "https://multica.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(workflow.DefaultConfig(), deps)
	if err := d.Start(); err != nil {
		t.Errorf("Start() = %v, want nil", err)
	}
	if err := d.Health(); err != nil {
		t.Errorf("Health() = %v, want nil", err)
	}
	if err := d.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}

func TestDriverHoldsConfigAndDeps(t *testing.T) {
	cfg := workflow.Config{MulticaBaseURL: "https://cfg.example.com"}
	deps := &Dependencies{
		MulticaBaseURL: "https://multica.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(cfg, deps)
	if d.cfg.MulticaBaseURL != cfg.MulticaBaseURL {
		t.Errorf("cfg.MulticaBaseURL = %q, want %q", d.cfg.MulticaBaseURL, cfg.MulticaBaseURL)
	}
	if d.deps != deps {
		t.Error("deps mismatch")
	}
}

func TestDriverStartRequiresDeps(t *testing.T) {
	d := NewDriver(workflow.DefaultConfig(), nil)
	if err := d.Start(); err == nil {
		t.Fatal("expected error when deps nil")
	}
}

func TestDriverTokenProviderNilDeps(t *testing.T) {
	d := NewDriver(workflow.DefaultConfig(), nil)
	if d.tokenProvider() != nil {
		t.Fatal("expected nil tokenProvider when deps is nil")
	}
}

func TestDriverAbortTask(t *testing.T) {
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
	d := NewDriver(cfg, &Dependencies{
		MulticaBaseURL: multica.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		runErr = d.RunTask(context.Background(), workflow.TaskRunPayload{
			TaskID:      "task-1",
			WorkspaceID: "ws-1",
			Agent:       "sh",
			Prompt:      "sleep 10",
		})
	}()

	time.Sleep(100 * time.Millisecond)
	if err := d.AbortTask("task-1"); err != nil {
		t.Fatalf("abort: %v", err)
	}

	wg.Wait()
	if runErr == nil {
		t.Fatal("expected RunTask to return an error after abort")
	}
}

