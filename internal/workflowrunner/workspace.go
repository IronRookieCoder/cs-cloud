package workflowrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const gitTimeout = 5 * time.Minute

// WorkspaceManager manages workspace directories and repo caches.
type WorkspaceManager struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewWorkspaceManager creates a new WorkspaceManager rooted at root.
func NewWorkspaceManager(root string) *WorkspaceManager {
	return &WorkspaceManager{root: root, locks: make(map[string]*sync.Mutex)}
}

// validateID rejects IDs that could escape wm.root via path traversal. IDs must
// be a single safe path component: no separators, no "."/"..".
func validateID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("invalid id %q", id)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid id %q", id)
	}
	if strings.Contains(filepath.Clean(id), string(filepath.Separator)) {
		return fmt.Errorf("invalid id %q", id)
	}
	return nil
}

// lockFor returns a per-path mutex used to serialize repo-cache provisioning so
// concurrent tasks for the same repo do not race through RemoveAll + clone.
func (wm *WorkspaceManager) lockFor(path string) *sync.Mutex {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	lock, ok := wm.locks[path]
	if !ok {
		lock = &sync.Mutex{}
		wm.locks[path] = lock
	}
	return lock
}

// EnsureRoot creates the root directory if it does not exist.
func (wm *WorkspaceManager) EnsureRoot() error {
	return os.MkdirAll(wm.root, 0o755)
}

// WorkspaceDir returns the directory for a workspace.
func (wm *WorkspaceManager) WorkspaceDir(workspaceID string) string {
	return filepath.Join(wm.root, workspaceID)
}

// RepoCacheDir returns the directory where repo mirrors are cached.
func (wm *WorkspaceManager) RepoCacheDir(workspaceID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "repos")
}

// TaskWorktreeDir returns the worktree directory for a task.
func (wm *WorkspaceManager) TaskWorktreeDir(workspaceID, taskID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "tasks", taskID)
}

// EnsureRepoReady ensures a mirror clone of repoURL exists for the workspace.
// On first use it clones; on subsequent calls it updates the existing mirror.
func (wm *WorkspaceManager) EnsureRepoReady(workspaceID, repoURL string) (string, error) {
	if repoURL == "" {
		return "", nil
	}
	if err := validateID(workspaceID); err != nil {
		return "", err
	}
	cacheDir := wm.RepoCacheDir(workspaceID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	name := repoName(repoURL)
	cache := filepath.Join(cacheDir, name)

	// Serialize provisioning per cache path: concurrent tasks for one repo can
	// both observe a missing HEAD, then race through RemoveAll and git clone.
	cacheLock := wm.lockFor(cache)
	cacheLock.Lock()
	defer cacheLock.Unlock()

	head := filepath.Join(cache, "HEAD")
	if _, err := os.Stat(head); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// Remove any incomplete cache left by an interrupted clone, then clone.
		_ = os.RemoveAll(cache)
		if err := runGit("clone", "--mirror", repoURL, cache); err != nil {
			return "", fmt.Errorf("clone repo: %w", err)
		}
	} else {
		if err := runGit("-C", cache, "remote", "update"); err != nil {
			return "", fmt.Errorf("update repo: %w", err)
		}
	}
	return cache, nil
}

// CreateWorktree ensures the repo cache is ready and creates a git worktree
// for the task at the given ref. If repoURL is empty, it simply creates the
// task directory without a git worktree.
func (wm *WorkspaceManager) CreateWorktree(workspaceID, taskID, repoURL, ref string) (string, error) {
	if err := validateID(workspaceID); err != nil {
		return "", err
	}
	if err := validateID(taskID); err != nil {
		return "", err
	}
	dir := wm.TaskWorktreeDir(workspaceID, taskID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}

	if repoURL == "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}

	cache, err := wm.EnsureRepoReady(workspaceID, repoURL)
	if err != nil {
		return "", err
	}

	// If the worktree exists but was created for a different ref, remove it
	// so the worktree matches the requested ref.
	if existingRef, _ := readWorktreeRef(dir); existingRef != "" && existingRef != ref {
		_ = runGit("-C", cache, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}

	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := runGit("-C", cache, "worktree", "add", dir, ref); err != nil {
		return "", fmt.Errorf("add worktree: %w", err)
	}
	if err := writeWorktreeRef(dir, ref); err != nil {
		return "", fmt.Errorf("write worktree ref marker: %w", err)
	}
	return dir, nil
}

// repoName extracts the repository name from a URL.
func repoName(url string) string {
	base := filepath.Base(url)
	if ext := filepath.Ext(base); ext == ".git" {
		return base[:len(base)-len(".git")]
	}
	return base
}

// runGit runs a git command with a default timeout and returns a wrapped error
// on failure.
func runGit(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return runGitCtx(ctx, args...)
}

// runGitCtx runs a git command under the provided context.
func runGitCtx(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}

func worktreeRefPath(dir string) string {
	return filepath.Join(dir, ".cs-workflow-ref")
}

func readWorktreeRef(dir string) (string, error) {
	b, err := os.ReadFile(worktreeRefPath(dir))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func writeWorktreeRef(dir, ref string) error {
	return os.WriteFile(worktreeRefPath(dir), []byte(ref+"\n"), 0o644)
}
