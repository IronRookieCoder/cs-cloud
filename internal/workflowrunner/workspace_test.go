package workflowrunner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available: ", err)
	}
}

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

func TestRepoName(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://example.com/repo.git", "repo"},
		{"https://example.com/repo", "repo"},
		{"git@example.com:org/repo.git", "repo"},
	}
	for _, tc := range cases {
		if got := repoName(tc.url); got != tc.want {
			t.Fatalf("repoName(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestWorkspaceManagerEnsureRepoReady(t *testing.T) {
	requireGit(t)

	upstream := initTestRepo(t)
	root := t.TempDir()
	wm := NewWorkspaceManager(root)

	cache, err := wm.EnsureRepoReady("ws-1", upstream, "")
	if err != nil {
		t.Fatalf("first ensure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
		t.Fatalf("mirror not created: %v", err)
	}

	// Second call should update the existing mirror without error.
	cache2, err := wm.EnsureRepoReady("ws-1", upstream, "")
	if err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	if cache2 != cache {
		t.Fatalf("cache path changed: %q -> %q", cache, cache2)
	}
}

func TestWorkspaceManagerCreateWorktree(t *testing.T) {
	requireGit(t)

	upstream := initTestRepo(t)
	root := t.TempDir()
	wm := NewWorkspaceManager(root)

	dir, err := wm.CreateWorktree("ws-1", "task-1", upstream, "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}

	// Re-creating should return the existing directory.
	dir2, err := wm.CreateWorktree("ws-1", "task-1", upstream, "HEAD")
	if err != nil {
		t.Fatalf("create worktree again: %v", err)
	}
	if dir2 != dir {
		t.Fatalf("worktree dir changed: %q -> %q", dir, dir2)
	}
}

func TestWorkspaceManagerCreateWorktreeWithoutRepo(t *testing.T) {
	root := t.TempDir()
	wm := NewWorkspaceManager(root)

	dir, err := wm.CreateWorktree("ws-1", "task-1", "", "HEAD")
	if err != nil {
		t.Fatalf("create worktree: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("task dir not created: %v", err)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitOrFatal(t, "init", dir)
	runGitOrFatal(t, "-C", dir, "config", "user.email", "test@example.com")
	runGitOrFatal(t, "-C", dir, "config", "user.name", "Test")
	path := filepath.Join(dir, "README.md")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGitOrFatal(t, "-C", dir, "add", "README.md")
	runGitOrFatal(t, "-C", dir, "commit", "-m", "init")
	return dir
}

func runGitOrFatal(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestEnsureRepoReady_WithAccessToken(t *testing.T) {
	requireGit(t)
	upstream := initTestRepo(t)
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	// A fake token is injected into the URL, but for a local file/path upstream
	// git ignores HTTP credentials; this just verifies the token-bearing path
	// doesn't error and the cache is built.
	cache, err := wm.EnsureRepoReady("ws-1", upstream, "fake-token")
	if err != nil {
		t.Fatalf("ensure repo: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
		t.Fatalf("mirror cache HEAD missing: %v", err)
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"产品经理":       "agent", // non-ascii collapses to nothing -> fallback "agent"
		"Backend / API": "backend-api",
		"Foo.Bar_Baz":   "foo-bar-baz",
		"":              "agent",
	}
	for in, want := range cases {
		if got := sanitizeName(in); got != want {
			t.Errorf("sanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestShortID(t *testing.T) {
	got := shortID("11111111-2222-3333-4444-555555555555")
	if got != "11111111" {
		t.Errorf("shortID = %q, want 11111111", got)
	}
}

func TestResetWorktree(t *testing.T) {
	requireGit(t)
	upstream := initTestRepo(t)
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	cache, _ := wm.EnsureRepoReady("ws-1", upstream, "")
	workDir := filepath.Join(root, "ws-1", "taskdir", "repo")
	if err := os.MkdirAll(filepath.Dir(workDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runGit("-C", cache, "worktree", "add", "-b", "first", workDir, "HEAD"); err != nil {
		t.Fatalf("worktree add: %v", err)
	}
	// Pollute: an untracked file + confirm clean state after reset.
	if err := os.WriteFile(filepath.Join(workDir, "junk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := wm.ResetWorktree(workDir, "second", "HEAD"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, "junk.txt")); !os.IsNotExist(err) {
		t.Errorf("junk.txt should be cleaned, got %v", err)
	}
	out, err := exec.Command("git", "-C", workDir, "branch", "--show-current").CombinedOutput()
	if err != nil {
		t.Fatalf("branch show: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "second" {
		t.Errorf("branch = %q, want second", strings.TrimSpace(string(out)))
	}
}
