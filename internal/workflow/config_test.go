package workflow

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultWorkflowConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.BackendBaseURL != "" {
		t.Fatalf("BackendBaseURL = %q, want empty", cfg.BackendBaseURL)
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

// TestDefaultConfigGC locks the GC field defaults that the gcLoop reads. They
// mirror the server's daemon defaults so cs-cloud reclaims at the same cadence.
func TestDefaultConfigGC(t *testing.T) {
	c := DefaultConfig()

	if !c.GCEnabled {
		t.Errorf("GCEnabled: want true, got false")
	}
	if c.GCTTL != 24*time.Hour {
		t.Errorf("GCTTL: want 24h, got %v", c.GCTTL)
	}
	if c.GCOrphanTTL != 72*time.Hour {
		t.Errorf("GCOrphanTTL: want 72h, got %v", c.GCOrphanTTL)
	}
	if c.GCArtifactTTL != 12*time.Hour {
		t.Errorf("GCArtifactTTL: want 12h, got %v", c.GCArtifactTTL)
	}
	wantPatterns := []string{"node_modules", ".next", ".turbo"}
	if len(c.GCArtifactPatterns) != len(wantPatterns) {
		t.Fatalf("GCArtifactPatterns: want %v, got %v", wantPatterns, c.GCArtifactPatterns)
	}
	for i, p := range wantPatterns {
		if c.GCArtifactPatterns[i] != p {
			t.Errorf("GCArtifactPatterns[%d]: want %q, got %q", i, p, c.GCArtifactPatterns[i])
		}
	}
}
