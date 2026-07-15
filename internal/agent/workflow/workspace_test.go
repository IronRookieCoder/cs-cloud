package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceManagerEnsureRoot(t *testing.T) {
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	if err := wm.EnsureRoot(); err != nil {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root not created: %v", err)
	}
}

func TestWorkspaceManagerPaths(t *testing.T) {
	root := t.TempDir()
	wm := NewWorkspaceManager(root)

	if got, want := wm.WorkspaceDir("ws-1"), filepath.Join(root, "ws-1"); got != want {
		t.Fatalf("WorkspaceDir = %q, want %q", got, want)
	}
	if got, want := wm.RepoCacheDir("ws-1"), filepath.Join(root, "ws-1", "repos"); got != want {
		t.Fatalf("RepoCacheDir = %q, want %q", got, want)
	}
	if got, want := wm.TaskWorktreeDir("ws-1", "task-1"), filepath.Join(root, "ws-1", "tasks", "task-1"); got != want {
		t.Fatalf("TaskWorktreeDir = %q, want %q", got, want)
	}
}

func TestWorkspaceManagerEnsureRepoReadyNotImplemented(t *testing.T) {
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	_, err := wm.EnsureRepoReady("ws-1", "https://example.com/repo.git")
	if err == nil {
		t.Fatal("expected not implemented error")
	}
}
