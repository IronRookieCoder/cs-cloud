package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheReadWriteWorkspaces(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)

	wss := []Workspace{{ID: "ws-1", Name: "Test"}}
	if err := c.WriteWorkspaces(wss); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := c.ReadWorkspaces()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ws-1" {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestCacheReadWorkspacesMissingFile(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)

	got, err := c.ReadWorkspaces()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %+v", got)
	}
}

func TestCacheReadWorkspacesInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	c := NewCache(dir)
	if err := os.WriteFile(filepath.Join(dir, "workspaces.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	if _, err := c.ReadWorkspaces(); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
