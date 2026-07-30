package execenv

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadGCMeta_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	meta := GCMeta{
		Kind:        GCKindWorkflowNodeRun,
		NodeRunID:   "nnnnnnnn-nnnn-nnnn-nnnn-nnnnnnnnnnnn",
		TaskID:      "tttttttt-tttt-tttt-tttt-tttttttttttt",
		WorkspaceID: "wwwwwwww-wwww-wwww-wwww-wwwwwwwwwwww",
	}
	if err := WriteGCMeta(dir, meta); err != nil {
		t.Fatalf("WriteGCMeta: %v", err)
	}
	got, err := ReadGCMeta(dir)
	if err != nil {
		t.Fatalf("ReadGCMeta: %v", err)
	}
	if got.Kind != GCKindWorkflowNodeRun {
		t.Errorf("Kind: want %q, got %q", GCKindWorkflowNodeRun, got.Kind)
	}
	if got.NodeRunID != meta.NodeRunID {
		t.Errorf("NodeRunID: want %q, got %q", meta.NodeRunID, got.NodeRunID)
	}
	if got.WorkspaceID != meta.WorkspaceID {
		t.Errorf("WorkspaceID: want %q, got %q", meta.WorkspaceID, got.WorkspaceID)
	}
	if got.CompletedAt.IsZero() {
		t.Error("CompletedAt should be stamped by WriteGCMeta when unset")
	}
}

func TestReadGCMeta_LegacyNoKindDefaultsIssue(t *testing.T) {
	dir := t.TempDir()
	// Hand-write a pre-kind meta file (only issue_id, like a legacy server file).
	if err := os.WriteFile(filepath.Join(dir, gcMetaFile), []byte(`{"issue_id":"iiii"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGCMeta(dir)
	if err != nil {
		t.Fatalf("ReadGCMeta: %v", err)
	}
	if got.Kind != GCKindIssue {
		t.Errorf("legacy Kind: want %q, got %q", GCKindIssue, got.Kind)
	}
	if got.IssueID != "iiii" {
		t.Errorf("IssueID: want %q, got %q", "iiii", got.IssueID)
	}
}

func TestWriteGCMeta_EmptyKindSkips(t *testing.T) {
	dir := t.TempDir()
	if err := WriteGCMeta(dir, GCMeta{Kind: "", IssueID: "x"}); err != nil {
		t.Fatalf("WriteGCMeta: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, gcMetaFile)); !os.IsNotExist(err) {
		t.Fatalf("expected no meta file written for empty Kind, got %v", err)
	}
}

func TestWriteGCMeta_EmptyRootIsNoOp(t *testing.T) {
	if err := WriteGCMeta("", GCMeta{Kind: GCKindIssue}); err != nil {
		t.Fatalf("empty envRoot should be a no-op, got %v", err)
	}
}

func TestWriteGCMeta_PreservesExplicitCompletedAt(t *testing.T) {
	dir := t.TempDir()
	want := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := WriteGCMeta(dir, GCMeta{Kind: GCKindIssue, IssueID: "i", CompletedAt: want}); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGCMeta(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CompletedAt.Equal(want) {
		t.Errorf("CompletedAt: want %v, got %v", want, got.CompletedAt)
	}
}
