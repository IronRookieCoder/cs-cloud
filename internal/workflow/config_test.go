package workflow

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultWorkflowConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MulticaBaseURL != "" {
		t.Fatalf("MulticaBaseURL = %q, want empty", cfg.MulticaBaseURL)
	}
	if !filepath.IsAbs(cfg.WorkspacesRoot) {
		t.Fatalf("WorkspacesRoot is not absolute: %q", cfg.WorkspacesRoot)
	}
	if !strings.HasSuffix(filepath.ToSlash(cfg.WorkspacesRoot), "workflow/workspaces") {
		t.Fatalf("WorkspacesRoot suffix wrong: %q", cfg.WorkspacesRoot)
	}
	if !filepath.IsAbs(cfg.CacheDir) {
		t.Fatalf("CacheDir is not absolute: %q", cfg.CacheDir)
	}
	if !strings.HasSuffix(filepath.ToSlash(cfg.CacheDir), "workflow/cache") {
		t.Fatalf("CacheDir suffix wrong: %q", cfg.CacheDir)
	}
	if cfg.SyncInterval != 5*time.Minute {
		t.Fatalf("SyncInterval = %v", cfg.SyncInterval)
	}
	if cfg.GCInterval != 24*time.Hour {
		t.Fatalf("GCInterval = %v", cfg.GCInterval)
	}
	if cfg.HeartbeatInterval != 15*time.Second {
		t.Fatalf("HeartbeatInterval = %v", cfg.HeartbeatInterval)
	}
	if cfg.AgentTimeout != 30*time.Minute {
		t.Fatalf("AgentTimeout = %v", cfg.AgentTimeout)
	}
	if cfg.MaxConcurrentTasks != 20 {
		t.Fatalf("MaxConcurrentTasks = %d", cfg.MaxConcurrentTasks)
	}
}
