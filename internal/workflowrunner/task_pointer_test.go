package workflowrunner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunsDir(t *testing.T) {
	got := RunsDir(filepath.Join("tmp", "workspaces"))
	want := filepath.Join("tmp", "runs")
	if got != want {
		t.Fatalf("RunsDir = %q, want %q", got, want)
	}
}

func TestWriteAndReadTaskPointer(t *testing.T) {
	root := t.TempDir()
	const taskID = "task-1"
	const taskRoot = "/some/worktree"
	if err := WriteTaskPointer(root, taskID, taskRoot); err != nil {
		t.Fatalf("WriteTaskPointer: %v", err)
	}
	if got := ReadTaskPointer(root, taskID); got != taskRoot {
		t.Fatalf("ReadTaskPointer = %q, want %q", got, taskRoot)
	}
	// Overwrite is idempotent.
	if err := WriteTaskPointer(root, taskID, "/other"); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got := ReadTaskPointer(root, taskID); got != "/other" {
		t.Fatalf("after overwrite = %q, want /other", got)
	}
	// Remove.
	RemoveTaskPointer(root, taskID)
	if got := ReadTaskPointer(root, taskID); got != "" {
		t.Fatalf("after remove = %q, want empty", got)
	}
}

func TestReadTaskPointerMissing(t *testing.T) {
	if got := ReadTaskPointer(t.TempDir(), "nope"); got != "" {
		t.Fatalf("ReadTaskPointer missing = %q, want empty", got)
	}
}

func TestReadTaskPointerRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, []byte("/evil"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(RunsDir(root), "task-sym")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported on this host: %v", err)
	}
	if got := ReadTaskPointer(root, "task-sym"); got != "" {
		t.Fatalf("ReadTaskPointer symlink = %q, want empty (rejected)", got)
	}
}

func TestWriteTaskPointerRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if err := WriteTaskPointer(root, "../escape", "/x"); err == nil {
		t.Fatal("expected error for path-traversal task id")
	}
}
