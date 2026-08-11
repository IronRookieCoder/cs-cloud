package membertask

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Layout struct {
	ProfileRoot string
}

func (l Layout) StoreRoot() string { return filepath.Join(l.ProfileRoot, "member-task-store") }
func (l Layout) TasksRoot() string { return filepath.Join(l.ProfileRoot, "member-tasks") }
func (l Layout) StagingRoot() string {
	return filepath.Join(l.ProfileRoot, "member-task-staging")
}
func (l Layout) QuarantineRoot() string {
	return filepath.Join(l.ProfileRoot, "member-task-quarantine")
}
func (l Layout) HistoryRoot() string {
	return filepath.Join(l.ProfileRoot, "member-task-history")
}
func (l Layout) PrepareJournalsRoot() string {
	return filepath.Join(l.StoreRoot(), "prepare-journals")
}
func (l Layout) DeleteJournalsRoot() string {
	return filepath.Join(l.StoreRoot(), "delete-journals")
}
func (l Layout) TaskDir(k TaskKey) string {
	return TaskDirIn(l.TasksRoot(), k)
}

func TaskDirIn(root string, k TaskKey) string {
	return filepath.Join(root, k.CloudInstanceID, k.WorkspaceID, k.NodeRunID+"-"+string(k.Role))
}

func ValidateContainedPath(root, path string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return newTaskError("unsafe_task_path", "cannot resolve task root", err)
	}
	pathAbs, err := filepath.Abs(path)
	if err != nil {
		return newTaskError("unsafe_task_path", "cannot resolve task path", err)
	}
	resolvedRoot, err := resolveWithMissingTail(rootAbs)
	if err != nil {
		return newTaskError("unsafe_task_path", "cannot resolve task root links", err)
	}
	resolvedPath, err := resolveWithMissingTail(pathAbs)
	if err != nil {
		return newTaskError("unsafe_task_path", "cannot resolve task path links", err)
	}
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return newTaskError("unsafe_task_path", fmt.Sprintf("path is outside task root: %s", pathAbs), err)
	}
	return nil
}

func resolveWithMissingTail(path string) (string, error) {
	current := filepath.Clean(path)
	var tail []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return filepath.Clean(path), nil
		}
		tail = append(tail, filepath.Base(current))
		current = parent
	}
}
