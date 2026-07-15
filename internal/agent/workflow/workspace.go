package workflow

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// WorkspaceManager manages workspace directories and repo caches.
type WorkspaceManager struct {
	root string
}

// NewWorkspaceManager creates a new WorkspaceManager rooted at root.
func NewWorkspaceManager(root string) *WorkspaceManager {
	return &WorkspaceManager{root: root}
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
	cacheDir := wm.RepoCacheDir(workspaceID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	name := repoName(repoURL)
	cache := filepath.Join(cacheDir, name)

	if _, err := os.Stat(filepath.Join(cache, "HEAD")); err != nil {
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
	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := runGit("-C", cache, "worktree", "add", dir, ref); err != nil {
		return "", fmt.Errorf("add worktree: %w", err)
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

// runGit runs a git command and returns a wrapped error on failure.
func runGit(args ...string) error {
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}
