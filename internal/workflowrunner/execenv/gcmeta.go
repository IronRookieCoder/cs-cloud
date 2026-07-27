// Package execenv persists per-task GC metadata (.gc_meta.json) so the cs-cloud
// GC loop can decide whether a workdir is reclaimable. Ported from multica
// server/internal/daemon/execenv/execenv.go (GCMeta/Read/Write), with an added
// GCKindWorkflowNodeRun for cs-cloud's workflow node-run tasks.
package execenv

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// GCMetaKind identifies which parent record governs a task workdir's lifecycle.
// The GC loop dispatches its decision tree on this value.
type GCMetaKind string

const (
	GCKindIssue           GCMetaKind = "issue"
	GCKindChat            GCMetaKind = "chat"
	GCKindAutopilotRun    GCMetaKind = "autopilot_run"
	GCKindQuickCreate     GCMetaKind = "quick_create"
	GCKindWorkflowNodeRun GCMetaKind = "workflow_node_run" // cs-cloud: workflow task (issue_id NULL)
)

// GCMeta is persisted to .gc_meta.json inside the task root. It is a
// discriminated union keyed on Kind: only the ID field matching Kind is
// meaningful. Pre-kind files normalize to GCKindIssue on read for backward
// compatibility (mirrors multica).
type GCMeta struct {
	Kind           GCMetaKind `json:"kind,omitempty"`
	IssueID        string     `json:"issue_id,omitempty"`
	ChatSessionID  string     `json:"chat_session_id,omitempty"`
	AutopilotRunID string     `json:"autopilot_run_id,omitempty"`
	TaskID         string     `json:"task_id,omitempty"`
	NodeRunID      string     `json:"node_run_id,omitempty"` // cs-cloud addition
	WorkspaceID    string     `json:"workspace_id"`
	CompletedAt    time.Time  `json:"completed_at"`
}

const gcMetaFile = ".gc_meta.json"

// WriteGCMeta writes GC metadata into envRoot. Empty Kind is a silent no-op so
// a task that doesn't fit any known kind falls back to orphan-by-mtime. An
// explicit CompletedAt is preserved; otherwise now() is stamped.
func WriteGCMeta(envRoot string, meta GCMeta) error {
	if envRoot == "" {
		return nil
	}
	if meta.Kind == "" {
		return nil
	}
	if meta.CompletedAt.IsZero() {
		meta.CompletedAt = time.Now().UTC()
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal gc meta: %w", err)
	}
	return os.WriteFile(filepath.Join(envRoot, gcMetaFile), data, 0o644)
}

// ReadGCMeta reads GC metadata from a task root. Pre-kind files (no kind field)
// normalize to GCKindIssue so the legacy issue path keeps working.
func ReadGCMeta(envRoot string) (*GCMeta, error) {
	data, err := os.ReadFile(filepath.Join(envRoot, gcMetaFile))
	if err != nil {
		return nil, err
	}
	var meta GCMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	if meta.Kind == "" {
		meta.Kind = GCKindIssue
	}
	return &meta, nil
}
