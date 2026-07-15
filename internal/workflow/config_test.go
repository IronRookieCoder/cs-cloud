package workflow

import "testing"

func TestDefaultWorkflowConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.WorkspacesRoot == "" {
		t.Fatal("WorkspacesRoot should not be empty")
	}
	if cfg.SyncInterval == 0 {
		t.Fatal("SyncInterval should not be zero")
	}
	if cfg.GCInterval == 0 {
		t.Fatal("GCInterval should not be zero")
	}
	if cfg.AgentTimeout == 0 {
		t.Fatal("AgentTimeout should not be zero")
	}
	if cfg.MaxConcurrentTasks == 0 {
		t.Fatal("MaxConcurrentTasks should not be zero")
	}
}
