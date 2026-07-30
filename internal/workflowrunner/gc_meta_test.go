package workflowrunner

import (
	"testing"

	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner/execenv"
)

func TestGCMetaForTask(t *testing.T) {
	cases := []struct {
		name     string
		payload  workflow.TaskRunPayload
		wantKind execenv.GCMetaKind
		wantOK   bool
		check    func(m execenv.GCMeta) bool
	}{
		{name: "node-run wins even when issue also set",
			payload: workflow.TaskRunPayload{TaskID: "t1", WorkspaceID: "ws", IssueID: "i1", NodeRunID: "n1"},
			wantKind: execenv.GCKindWorkflowNodeRun, wantOK: true,
			check: func(m execenv.GCMeta) bool { return m.NodeRunID == "n1" && m.IssueID == "" }},
		{name: "issue only",
			payload: workflow.TaskRunPayload{TaskID: "t2", WorkspaceID: "ws", IssueID: "i2"},
			wantKind: execenv.GCKindIssue, wantOK: true,
			check: func(m execenv.GCMeta) bool { return m.IssueID == "i2" }},
		{name: "task only falls through to quick_create",
			payload: workflow.TaskRunPayload{TaskID: "t3", WorkspaceID: "ws"},
			wantKind: execenv.GCKindQuickCreate, wantOK: true,
			check: func(m execenv.GCMeta) bool { return m.TaskID == "t3" }},
		{name: "no IDs → ok=false",
			payload:  workflow.TaskRunPayload{WorkspaceID: "ws"},
			wantOK:   false,
			check:    func(m execenv.GCMeta) bool { return true }},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			meta, ok := gcMetaForTask(tc.payload)
			if ok != tc.wantOK {
				t.Fatalf("ok: want %v, got %v", tc.wantOK, ok)
			}
			if !tc.wantOK {
				return
			}
			if meta.Kind != tc.wantKind {
				t.Fatalf("kind: want %q, got %q", tc.wantKind, meta.Kind)
			}
			if meta.WorkspaceID != "ws" {
				t.Errorf("workspace_id: want ws, got %q", meta.WorkspaceID)
			}
			if !tc.check(meta) {
				t.Errorf("id field mismatch: %+v", meta)
			}
		})
	}
}
