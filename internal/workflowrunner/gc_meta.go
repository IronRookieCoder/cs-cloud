package workflowrunner

import (
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner/execenv"
)

// gcMetaForTask picks the GCKind + ID for a task's .gc_meta.json. Priority:
// workflow node-run (the most specific lifecycle parent — the workdir belongs
// to the node run's execution) > issue > quick-create (the task row itself).
// Returns ok=false when no ID is known; the caller skips the write and the
// dir falls back to orphan-by-mtime.
func gcMetaForTask(p workflow.TaskRunPayload) (execenv.GCMeta, bool) {
	switch {
	case p.NodeRunID != "":
		return execenv.GCMeta{
			Kind:        execenv.GCKindWorkflowNodeRun,
			NodeRunID:   p.NodeRunID,
			TaskID:      p.TaskID,
			WorkspaceID: p.WorkspaceID,
		}, true
	case p.IssueID != "":
		return execenv.GCMeta{
			Kind:        execenv.GCKindIssue,
			IssueID:     p.IssueID,
			TaskID:      p.TaskID,
			WorkspaceID: p.WorkspaceID,
		}, true
	case p.TaskID != "":
		return execenv.GCMeta{
			Kind:        execenv.GCKindQuickCreate,
			TaskID:      p.TaskID,
			WorkspaceID: p.WorkspaceID,
		}, true
	default:
		return execenv.GCMeta{}, false
	}
}

// writeGCMetaForTask writes (or rewrites) the task's .gc_meta.json. Pass a zero
// completedAt at prepare time — WriteGCMeta stamps now(). Pass the real finish
// time at completion so the TTLs anchor on when the task actually ended.
// Failures are best-effort: a missing meta just means the gcLoop falls back to
// orphan-by-mtime.
func writeGCMetaForTask(taskRoot string, payload workflow.TaskRunPayload, completedAt time.Time) {
	if taskRoot == "" {
		return
	}
	meta, ok := gcMetaForTask(payload)
	if !ok {
		return
	}
	meta.CompletedAt = completedAt
	if err := execenv.WriteGCMeta(taskRoot, meta); err != nil {
		logger.Warn("workflow: write gc meta failed: task=%s err=%v", payload.TaskID, err)
	}
}
