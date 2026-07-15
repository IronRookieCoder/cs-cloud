package workflow

import (
	"fmt"
	"os"
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

// EnsureRepoReady returns a not-implemented error in Phase 3.2.
func (wm *WorkspaceManager) EnsureRepoReady(workspaceID, repoURL string) (string, error) {
	return "", fmt.Errorf("not implemented")
}
