package workflowrunner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestOutbox(t *testing.T) *Outbox {
	t.Helper()
	return NewOutbox(t.TempDir())
}

func sampleFact(id string) OutboxFact {
	return OutboxFact{
		FactID:     id,
		TaskID:     "task-1",
		Kind:       "complete",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
		Output:     "done",
		SessionID:  "session-1",
		WorkDir:    "/tmp/wd",
	}
}

func TestOutbox_AddAndPending(t *testing.T) {
	o := newTestOutbox(t)
	f := sampleFact("fact-1")

	if err := o.Add(f); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact, got %d", len(pending))
	}
	if pending[0].FactID != f.FactID {
		t.Errorf("expected fact_id %q, got %q", f.FactID, pending[0].FactID)
	}
}

func TestOutbox_MarkDoneRemovesFromPending(t *testing.T) {
	o := newTestOutbox(t)
	f := sampleFact("fact-1")
	if err := o.Add(f); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if err := o.MarkDone(f.FactID); err != nil {
		t.Fatalf("MarkDone failed: %v", err)
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending failed: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending facts after MarkDone, got %d", len(pending))
	}
}

func TestOutbox_SurvivesRestart(t *testing.T) {
	appDir := t.TempDir()
	o1 := NewOutbox(appDir)
	f := sampleFact("fact-1")
	if err := o1.Add(f); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Simulate process restart by creating a new Outbox on the same directory.
	o2 := NewOutbox(appDir)
	pending, err := o2.Pending()
	if err != nil {
		t.Fatalf("Pending after restart failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact after restart, got %d", len(pending))
	}
	if pending[0].FactID != f.FactID {
		t.Errorf("expected fact_id %q after restart, got %q", f.FactID, pending[0].FactID)
	}
}

func TestOutbox_AddIdempotent(t *testing.T) {
	o := newTestOutbox(t)
	f := sampleFact("fact-1")
	if err := o.Add(f); err != nil {
		t.Fatalf("first Add failed: %v", err)
	}
	if err := o.Add(f); err != nil {
		t.Fatalf("second Add failed: %v", err)
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending failed: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact after duplicate Add, got %d", len(pending))
	}
}

func TestOutbox_PendingSkipsCorruptFiles(t *testing.T) {
	o := newTestOutbox(t)
	good := sampleFact("fact-good")
	if err := o.Add(good); err != nil {
		t.Fatalf("Add good fact failed: %v", err)
	}

	// Write a corrupt file into the pending directory.
	corruptPath := filepath.Join(o.pendingDir(), "corrupt.json")
	if err := os.WriteFile(corruptPath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending failed on corrupt file: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact with corrupt file skipped, got %d", len(pending))
	}
	if pending[0].FactID != good.FactID {
		t.Errorf("expected fact_id %q, got %q", good.FactID, pending[0].FactID)
	}
}
