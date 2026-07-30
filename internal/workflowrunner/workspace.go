package workflowrunner

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WorkspaceManager manages per-workspace task directories on disk. It no longer
// manages repo mirror caches or per-repo worktrees: the agent clones the repos
// it needs into the task root itself (guided by the task prompt + env vars), and
// the deliverable/MR CLIs locate a repo via the current working directory.
type WorkspaceManager struct {
	root string
}

// NewWorkspaceManager creates a new WorkspaceManager rooted at root.
func NewWorkspaceManager(root string) *WorkspaceManager {
	return &WorkspaceManager{root: root}
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

// EnsureRoot creates the root directory if it does not exist.
func (wm *WorkspaceManager) EnsureRoot() error {
	return os.MkdirAll(wm.root, 0o755)
}

// WorkspaceDir returns the directory for a workspace.
func (wm *WorkspaceManager) WorkspaceDir(workspaceID string) string {
	return filepath.Join(wm.root, workspaceID)
}

// TaskWorktreeDir returns the task directory for a task: <wsDir>/tasks/<taskID>.
// This is the agent's cwd ($CS_CLOUD_WORKTREE); the agent clones any repos it
// needs into here, and GC reclaims the whole directory when the task finalizes.
func (wm *WorkspaceManager) TaskWorktreeDir(workspaceID, taskID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "tasks", taskID)
}
