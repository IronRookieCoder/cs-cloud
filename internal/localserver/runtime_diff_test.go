package localserver

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTestFile creates a file (and any missing parent dirs) inside the
// test workspace.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// TestGitShowFileWorktreeReadsLegitimateFile exercises the happy path: a
// repo-relative path inside the workspace must be readable through the
// worktree branch.
func TestGitShowFileWorktreeReadsLegitimateFile(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "src", "main.go"), "package main\n")

	got := gitShowFile(dir, "worktree", "src/main.go")
	if got != "package main\n" {
		t.Fatalf("forward-slash form: got %q, want file content", got)
	}
	got = gitShowFile(dir, "worktree", filepath.Join("src", "main.go"))
	if got != "package main\n" {
		t.Fatalf("native-separator form: got %q, want file content", got)
	}
}

// TestGitShowFileWorktreeRejectsTraversal verifies that "../" escape attempts
// in the client-supplied path query param cannot reach files outside the
// workspace dir.
func TestGitShowFileWorktreeRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	// Plant a secret file in a sibling directory outside the workspace.
	parent := filepath.Dir(dir)
	outsideName := "outside-" + filepath.Base(dir) + "-secret.txt"
	outside := filepath.Join(parent, outsideName)
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}

	cases := []string{
		"../" + outsideName,
		"../../" + filepath.Base(parent) + "/" + outsideName,
		"./../" + outsideName,
	}
	for _, p := range cases {
		got := gitShowFile(dir, "worktree", p)
		if got != "" {
			t.Errorf("path %q: got %q, want empty (traversal rejected)", p, got)
		}
	}
}

// TestGitShowFileWorktreeRejectsHighRisk verifies the blacklist is enforced
// even when a secret-bearing file exists *inside* the workspace — an
// attacker who planted (or an admin who stored) credentials there must not
// be able to read them back through the diff-content endpoint.
func TestGitShowFileWorktreeRejectsHighRisk(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ".ssh", "id_rsa"), "PRIVATE KEY MATERIAL")

	if got := gitShowFile(dir, "worktree", ".ssh/id_rsa"); got != "" {
		t.Fatalf("got %q, want empty (blacklist should block)", got)
	}
}

// TestGitShowFileWorktreeRejectsSymlinkEscape verifies that a symlink inside
// the workspace pointing at a sensitive external target is refused. The
// target here is benign in name, but the resolved real path is outside the
// workspace — the only thing that should block this is the EvalSymlinks
// containment re-check.
func TestGitShowFileWorktreeRejectsSymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "outside-"+filepath.Base(dir)+"-target.txt")
	t.Cleanup(func() { _ = os.Remove(outside) })
	if err := os.WriteFile(outside, []byte("SECRET"), 0o600); err != nil {
		t.Fatalf("write outside: %v", err)
	}
	link := filepath.Join(dir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported in this environment: %v", err)
	}

	if got := gitShowFile(dir, "worktree", "escape"); got != "" {
		t.Fatalf("got %q, want empty (symlink escape rejected)", got)
	}
}

// TestGitShowFileWorktreeEmptyPathReturnsEmpty guards the early-return at the
// top of gitShowFile.
func TestGitShowFileWorktreeEmptyPathReturnsEmpty(t *testing.T) {
	if got := gitShowFile(t.TempDir(), "worktree", ""); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// TestGitShowFileWorktreeMissingFileReturnsEmpty documents that a benign
// non-existent path simply yields an empty string (the handler treats this
// as "no before/after content"), not an error.
func TestGitShowFileWorktreeMissingFileReturnsEmpty(t *testing.T) {
	if got := gitShowFile(t.TempDir(), "worktree", "does-not-exist.txt"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}
