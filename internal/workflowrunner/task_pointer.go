package workflowrunner

import (
	"os"
	"path/filepath"
	"strings"
)

// RunsDir is the directory holding per-task pointer files (<runs>/<taskID> →
// the task's real root path). It is a sibling of the workspaces root so both
// the runner (writer) and the in-task CLI (reader) derive the same location
// from the same config value.
func RunsDir(workspacesRoot string) string {
	return filepath.Join(filepath.Dir(workspacesRoot), "runs")
}

// WriteTaskPointer records the task's real root path at <runs>/<taskID> so the
// in-task CLI can locate .cs-cloud.env in O(1) by task id (instead of scanning
// every task dir). Idempotent — overwrites. The pointer is removed by
// RemoveTaskPointer when the session ends; orphan pointers left by a process
// crash are tiny, ignored by the scan fallback, and age out with the workdir.
// taskID is validated (single path component) so it cannot escape RunsDir.
func WriteTaskPointer(workspacesRoot, taskID, taskRoot string) error {
	if err := validateID(taskID); err != nil {
		return err
	}
	dir := RunsDir(workspacesRoot)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, taskID), []byte(taskRoot+"\n"), 0o600)
}

// RemoveTaskPointer deletes the pointer for taskID. Best-effort: a missing file
// (already removed, or never written) is silently ignored.
func RemoveTaskPointer(workspacesRoot, taskID string) {
	if err := validateID(taskID); err != nil {
		return
	}
	_ = os.Remove(filepath.Join(RunsDir(workspacesRoot), taskID))
}

// ReadTaskPointer returns the task root path recorded at <runs>/<taskID>, or ""
// when absent, unreadable, or a symlink. The symlink rejection prevents an
// attacker who can write into RunsDir from redirecting the CLI at a chosen
// .cs-cloud.env (which would let it pin CS_CLOUD_LOCAL_URL). Callers treat "" as
// "pointer missing/stale — fall back to scanning".
func ReadTaskPointer(workspacesRoot, taskID string) string {
	if err := validateID(taskID); err != nil {
		return ""
	}
	path := filepath.Join(RunsDir(workspacesRoot), taskID)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
