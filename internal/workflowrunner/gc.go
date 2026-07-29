package workflowrunner

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/workflowrunner/execenv"
)

// gcAPITimeout bounds each gc-check HTTP call so a slow server can't stall the
// whole GC cycle.
const gcAPITimeout = 10 * time.Second

// gitCmdTimeout bounds `git worktree prune` invocations.
const gitCmdTimeout = 30 * time.Second

// runGC performs a single GC scan across all workspace directories. It is the
// gcFunc plugged into runtimeLoop. Ported from the server
// server/internal/daemon/gc.go, adapted to cs-cloud's directory layout
// (<workspacesRoot>/<wsID>/tasks/<taskID> workdirs, <wsID>/repos/ bare cache).
func (d *Driver) runGC() error {
	if !d.cfg.GCEnabled {
		return nil
	}
	root := d.cfg.WorkspacesRoot
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		logger.Warn("gc: read workspaces root failed: %v", err)
		return nil
	}

	stats := &gcStats{byPattern: map[string]int{}}
	for _, wsEntry := range entries {
		if !wsEntry.IsDir() {
			continue
		}
		wsDir := filepath.Join(root, wsEntry.Name())
		d.gcWorkspace(wsDir, stats)
	}

	if stats.cleaned > 0 || stats.orphaned > 0 || stats.artifactDirs > 0 {
		logger.Info("gc: cycle complete: cleaned=%d orphaned=%d skipped=%d artifact_dirs=%d artifact_removed=%d bytes_reclaimed=%d by_pattern=%v",
			stats.cleaned, stats.orphaned, stats.skipped, stats.artifactDirs, stats.artifactRemoved, stats.bytesReclaimed, stats.byPattern)
	}
	return nil
}

// gcStats accumulates byte counts and per-pattern hit counts for one GC cycle.
type gcStats struct {
	cleaned         int            // whole task dirs removed (terminal + stale)
	orphaned        int            // whole task dirs removed (no meta / unreachable)
	skipped         int            // task dirs left untouched
	artifactDirs    int            // task dirs that had at least one artifact reclaimed
	artifactRemoved int            // count of removed artifact subdirs
	bytesReclaimed  int64          // total bytes freed in this cycle
	byPattern       map[string]int // basename -> reclaim count, for visibility
}

// gcWorkspace scans task directories inside a single workspace's tasks/ dir.
func (d *Driver) gcWorkspace(wsDir string, stats *gcStats) {
	tasksDir := filepath.Join(wsDir, "tasks")
	taskEntries, err := os.ReadDir(tasksDir)
	if err != nil {
		// No tasks/ subdir yet — nothing to GC here. Not an error.
		return
	}

	cleanedHere := 0
	for _, entry := range taskEntries {
		if !entry.IsDir() {
			continue
		}
		taskDir := filepath.Join(tasksDir, entry.Name())
		switch d.shouldCleanTaskDir(taskDir) {
		case gcActionClean:
			// TOCTOU re-check: shouldCleanTaskDir's isActiveEnvRoot gate ran
			// before an HTTP gc-check round-trip (up to gcAPITimeout) and a
			// dirSize walk; a task resuming via prior_work_dir can claim this
			// exact dir inside that window. Re-verify under d.mu immediately
			// before the destructive op so we never wipe an in-flight task.
			if d.isActiveEnvRoot(taskDir) {
				stats.skipped++
				continue
			}
			bytes := dirSize(taskDir)
			d.cleanTaskDir(taskDir)
			stats.cleaned++
			stats.bytesReclaimed += bytes
			cleanedHere++
		case gcActionOrphan:
			if d.isActiveEnvRoot(taskDir) {
				stats.skipped++
				continue
			}
			bytes := dirSize(taskDir)
			d.cleanTaskDir(taskDir)
			stats.orphaned++
			stats.bytesReclaimed += bytes
			cleanedHere++
		case gcActionCleanArtifacts:
			if d.isActiveEnvRoot(taskDir) {
				stats.skipped++
				continue
			}
			removed, bytes, perPattern := d.cleanTaskArtifacts(taskDir, d.cfg.GCArtifactPatterns)
			if removed > 0 {
				stats.artifactDirs++
				stats.artifactRemoved += removed
				stats.bytesReclaimed += bytes
				for k, v := range perPattern {
					stats.byPattern[k] += v
				}
			}
			stats.skipped++ // task dir itself preserved
		default:
			stats.skipped++
		}
	}

	// Drop the tasks/ dir if it is now empty. Leave the workspace dir itself —
	// it may still hold the repos/ bare cache for in-flight checkouts.
	if cleanedHere > 0 {
		if remaining, _ := os.ReadDir(tasksDir); len(remaining) == 0 {
			os.Remove(tasksDir)
		}
	}
}

type gcAction int

const (
	gcActionSkip           gcAction = iota
	gcActionClean                   // parent terminal and stale
	gcActionOrphan                  // no meta or unreachable parent and dir is old
	gcActionCleanArtifacts          // task completed long enough ago; drop regenerable artifacts only
)

// shouldCleanTaskDir decides whether a task directory should be removed.
// Dispatches on meta.Kind so each parent-record type follows its own lifecycle.
func (d *Driver) shouldCleanTaskDir(taskDir string) gcAction {
	// A task currently running on this env root must never be reclaimed — not
	// even on the terminal or 404 paths. A follow-up comment on an already-done
	// issue can dispatch a task reusing the prior workdir without bumping
	// updated_at, so the TTL check alone wouldn't notice the resumed activity.
	if d.isActiveEnvRoot(taskDir) {
		return gcActionSkip
	}

	meta, err := execenv.ReadGCMeta(taskDir)
	if err != nil {
		return d.orphanByMTime(taskDir, "no meta")
	}

	switch meta.Kind {
	case execenv.GCKindIssue:
		return d.gcDecisionIssue(taskDir, meta)
	case execenv.GCKindChat:
		return d.gcDecisionChat(taskDir, meta)
	case execenv.GCKindAutopilotRun:
		return d.gcDecisionAutopilotRun(taskDir, meta)
	case execenv.GCKindQuickCreate:
		return d.gcDecisionQuickCreate(taskDir, meta)
	case execenv.GCKindWorkflowNodeRun:
		return d.gcDecisionWorkflowNodeRun(taskDir, meta)
	default:
		// Unknown kind: fall back to mtime-based orphan cleanup so a future
		// build writing a kind we don't recognize doesn't get insta-wiped.
		return d.orphanByMTime(taskDir, "unknown kind")
	}
}

// orphanByMTime returns gcActionOrphan if the directory is older than
// GCOrphanTTL, gcActionSkip otherwise.
func (d *Driver) orphanByMTime(taskDir, reason string) gcAction {
	info, err := os.Stat(taskDir)
	if err != nil {
		return gcActionSkip
	}
	if time.Since(info.ModTime()) > d.cfg.GCOrphanTTL {
		logger.Info("gc: orphan directory: dir=%s reason=%s age=%s", taskDir, reason, time.Since(info.ModTime()).Round(time.Hour))
		return gcActionOrphan
	}
	return gcActionSkip
}

// isAccessNotFound detects the 404 returned by gc-check endpoints. The same
// status covers "row deleted" and "token can't see this workspace" (the
// anti-enumeration shape), so callers can't tell the two apart from the
// response alone.
func isAccessNotFound(err error) bool {
	var stErr *StatusError
	return errors.As(err, &stErr) && stErr.StatusCode == http.StatusNotFound
}

func (d *Driver) gcDecisionIssue(taskDir string, meta *execenv.GCMeta) gcAction {
	ctx, cancel := context.WithTimeout(context.Background(), gcAPITimeout)
	defer cancel()
	status, err := d.client.GetIssueGCCheck(ctx, meta.IssueID)
	if err != nil {
		if isAccessNotFound(err) {
			// 404 is ambiguous (deleted vs scoped token); fall back to
			// mtime-gated orphan so a scoped token can't instantly wipe dirs
			// whose issues are still live.
			return d.orphanByMTime(taskDir, "issue not accessible")
		}
		return gcActionSkip
	}

	if (status.Status == "done" || status.Status == "cancelled") &&
		time.Since(status.UpdatedAt) > d.cfg.GCTTL {
		logger.Info("gc: eligible for cleanup: dir=%s kind=issue issue=%s status=%s updated_at=%s",
			filepath.Base(taskDir), meta.IssueID, status.Status, status.UpdatedAt.Format(time.RFC3339))
		return gcActionClean
	}

	if d.cfg.GCArtifactTTL > 0 && len(d.cfg.GCArtifactPatterns) > 0 &&
		!meta.CompletedAt.IsZero() && time.Since(meta.CompletedAt) > d.cfg.GCArtifactTTL {
		logger.Info("gc: eligible for artifact cleanup: dir=%s kind=issue issue=%s status=%s completed_at=%s",
			filepath.Base(taskDir), meta.IssueID, status.Status, meta.CompletedAt.Format(time.RFC3339))
		return gcActionCleanArtifacts
	}

	return gcActionSkip
}

func (d *Driver) gcDecisionChat(taskDir string, meta *execenv.GCMeta) gcAction {
	ctx, cancel := context.WithTimeout(context.Background(), gcAPITimeout)
	defer cancel()
	status, err := d.client.GetChatSessionGCCheck(ctx, meta.ChatSessionID)
	if err != nil {
		if isAccessNotFound(err) {
			// 404 means the chat_session row is gone — a hard delete is the
			// strongest reclaim signal. No mtime gate: every id in a meta file
			// was written by this daemon under its current token.
			logger.Info("gc: eligible for cleanup: dir=%s kind=chat chat_session=%s reason=session not accessible (hard-deleted)",
				filepath.Base(taskDir), meta.ChatSessionID)
			return gcActionClean
		}
		return gcActionSkip
	}

	switch status.Status {
	case "active":
		return gcActionSkip
	case "archived":
		if time.Since(status.UpdatedAt) > d.cfg.GCTTL {
			logger.Info("gc: eligible for cleanup: dir=%s kind=chat chat_session=%s status=%s updated_at=%s",
				filepath.Base(taskDir), meta.ChatSessionID, status.Status, status.UpdatedAt.Format(time.RFC3339))
			return gcActionClean
		}
	}
	return gcActionSkip
}

func (d *Driver) gcDecisionAutopilotRun(taskDir string, meta *execenv.GCMeta) gcAction {
	ctx, cancel := context.WithTimeout(context.Background(), gcAPITimeout)
	defer cancel()
	status, err := d.client.GetAutopilotRunGCCheck(ctx, meta.AutopilotRunID)
	if err != nil {
		if isAccessNotFound(err) {
			return d.orphanByMTime(taskDir, "autopilot run not accessible")
		}
		return gcActionSkip
	}

	if isAutopilotRunTerminal(status.Status) {
		anchor := status.CompletedAt
		if anchor.IsZero() {
			anchor = meta.CompletedAt
		}
		if !anchor.IsZero() && time.Since(anchor) > d.cfg.GCTTL {
			logger.Info("gc: eligible for cleanup: dir=%s kind=autopilot_run run=%s status=%s completed_at=%s",
				filepath.Base(taskDir), meta.AutopilotRunID, status.Status, anchor.Format(time.RFC3339))
			return gcActionClean
		}
	}
	return gcActionSkip
}

// isAutopilotRunTerminal mirrors the run.status terminal set.
func isAutopilotRunTerminal(status string) bool {
	switch status {
	case "completed", "failed", "skipped", "issue_created":
		return true
	default:
		return false
	}
}

func (d *Driver) gcDecisionQuickCreate(taskDir string, meta *execenv.GCMeta) gcAction {
	ctx, cancel := context.WithTimeout(context.Background(), gcAPITimeout)
	defer cancel()
	status, err := d.client.GetTaskGCCheck(ctx, meta.TaskID)
	if err != nil {
		if isAccessNotFound(err) {
			return d.orphanByMTime(taskDir, "task not accessible")
		}
		return gcActionSkip
	}

	// Quick-create workdirs are not reused by a later issue task, so as soon as
	// the task itself reaches a terminal state the directory can be reclaimed.
	if isAgentTaskTerminal(status.Status) {
		logger.Info("gc: eligible for cleanup: dir=%s kind=quick_create task=%s status=%s",
			filepath.Base(taskDir), meta.TaskID, status.Status)
		return gcActionClean
	}
	return gcActionSkip
}

// isAgentTaskTerminal reports whether a value of agent_task_queue.status is final.
func isAgentTaskTerminal(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

// gcDecisionWorkflowNodeRun reclaims workdirs for cs-cloud's workflow tasks
// (issue_id NULL, workflow_node_run_id set). Terminal node-run statuses past
// GCTTL → clean; 404 (run or parent workflow_run deleted) → orphan-by-mtime.
func (d *Driver) gcDecisionWorkflowNodeRun(taskDir string, meta *execenv.GCMeta) gcAction {
	ctx, cancel := context.WithTimeout(context.Background(), gcAPITimeout)
	defer cancel()
	status, err := d.client.GetWorkflowNodeRunGCCheck(ctx, meta.NodeRunID)
	if err != nil {
		if isAccessNotFound(err) {
			return d.orphanByMTime(taskDir, "workflow node run not accessible")
		}
		return gcActionSkip
	}

	if isWorkflowNodeRunTerminal(status.Status) {
		anchor := status.CompletedAt
		if anchor.IsZero() {
			anchor = meta.CompletedAt
		}
		if !anchor.IsZero() && time.Since(anchor) > d.cfg.GCTTL {
			logger.Info("gc: eligible for cleanup: dir=%s kind=workflow_node_run node_run=%s status=%s completed_at=%s",
				filepath.Base(taskDir), meta.NodeRunID, status.Status, anchor.Format(time.RFC3339))
			return gcActionClean
		}
	}
	return gcActionSkip
}

// isWorkflowNodeRunTerminal mirrors the workflow_node_run terminal set (the
// statuses cancelWorkflowNodeRuns treats as already-done).
func isWorkflowNodeRunTerminal(status string) bool {
	switch status {
	case "completed", "failed", "blocked", "skipped", "cancelled", "format_failed":
		return true
	default:
		return false
	}
}

// cleanTaskDir removes a task directory and logs the result.
func (d *Driver) cleanTaskDir(taskDir string) {
	if err := os.RemoveAll(taskDir); err != nil {
		logger.Warn("gc: remove task dir failed: dir=%s err=%v", taskDir, err)
	} else {
		logger.Info("gc: removed: dir=%s", taskDir)
	}
}

// cleanTaskArtifacts walks taskDir and deletes every directory whose basename
// matches one of patterns. Returns (removedCount, bytesReclaimed, perPattern).
//
// Safety contract:
//   - patterns are basename-only; entries with a path separator are dropped.
//   - .git subtrees are never descended into.
//   - symlinks are skipped entirely.
//   - every removal target is verified to live inside taskDir.
func (d *Driver) cleanTaskArtifacts(taskDir string, patterns []string) (removed int, bytes int64, perPattern map[string]int) {
	perPattern = map[string]int{}
	if taskDir == "" || len(patterns) == 0 {
		return
	}
	patternSet := make(map[string]struct{}, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" || strings.ContainsAny(p, "/\\") {
			continue
		}
		patternSet[p] = struct{}{}
	}
	if len(patternSet) == 0 {
		return
	}

	absRoot, err := filepath.Abs(taskDir)
	if err != nil {
		return
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil // best-effort — keep walking
		}
		if path == absRoot {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if entry.Name() == ".git" {
			return filepath.SkipDir
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return filepath.SkipDir
		}
		if _, ok := patternSet[entry.Name()]; !ok {
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") {
			return filepath.SkipDir
		}
		size := dirSize(path)
		if rmErr := os.RemoveAll(path); rmErr != nil {
			logger.Warn("gc: artifact remove failed: path=%s err=%v", path, rmErr)
			return filepath.SkipDir
		}
		removed++
		bytes += size
		perPattern[entry.Name()]++
		logger.Info("gc: artifact removed: path=%s bytes=%d", path, size)
		return filepath.SkipDir
	})
	if walkErr != nil {
		logger.Warn("gc: artifact walk failed: dir=%s err=%v", taskDir, walkErr)
	}
	return
}

// dirSize returns the total size of all regular files under root, in bytes.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

