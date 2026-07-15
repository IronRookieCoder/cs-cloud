package workflow

import (
	"testing"

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
