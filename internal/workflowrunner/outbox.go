package workflowrunner

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OutboxFact is a durable, device-side record of a workflow task outcome.
// JSON field names match the multica server TaskFact contract so the delivery
// loop can post the file body with minimal translation.
type OutboxFact struct {
	FactID        string    `json:"fact_id"`
	TaskID        string    `json:"task_id"`
	Kind          string    `json:"kind"`
	OccurredAt    time.Time `json:"occurred_at"`
	Output        string    `json:"output,omitempty"`
	SessionID     string    `json:"session_id,omitempty"`
	WorkDir       string    `json:"work_dir,omitempty"`
	Decision      string    `json:"decision,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	Error         string    `json:"error,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	Attempts      int       `json:"attempts"`
}

// Outbox is a file-backed durable outbox for workflow task facts.
// Facts are atomically written to a pending directory; once delivered they are
// moved to a done directory. The outbox survives process restarts.
type Outbox struct {
	dir string
}

// NewOutbox creates an outbox rooted at <appDir>/workflow/outbox.
func NewOutbox(appDir string) *Outbox {
	return &Outbox{dir: filepath.Join(appDir, "workflow", "outbox")}
}

func (o *Outbox) pendingDir() string { return filepath.Join(o.dir, "pending") }
func (o *Outbox) doneDir() string    { return filepath.Join(o.dir, "done") }

func (o *Outbox) ensureDirs() error {
	if err := os.MkdirAll(o.pendingDir(), 0o700); err != nil {
		return fmt.Errorf("create pending dir: %w", err)
	}
	if err := os.MkdirAll(o.doneDir(), 0o700); err != nil {
		return fmt.Errorf("create done dir: %w", err)
	}
	return nil
}

// Add persists a fact to the pending directory. The write is atomic (temp file
// plus rename) and idempotent by fact_id: re-adding the same fact_id overwrites
// the existing file but never creates a duplicate.
func (o *Outbox) Add(f OutboxFact) error {
	if err := o.ensureDirs(); err != nil {
		return err
	}

	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("marshal fact: %w", err)
	}

	tmp, err := os.CreateTemp(o.pendingDir(), ".fact-*")
	if err != nil {
		return fmt.Errorf("create temp fact file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp fact file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp fact file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp fact file: %w", err)
	}

	target := filepath.Join(o.pendingDir(), f.FactID+".json")
	if err := os.Rename(tmpName, target); err != nil {
		return fmt.Errorf("install fact file: %w", err)
	}
	return nil
}

// Pending returns all facts currently in the pending directory. Corrupt files
// are logged and skipped rather than causing the scan to fail.
func (o *Outbox) Pending() ([]OutboxFact, error) {
	if err := o.ensureDirs(); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(o.pendingDir())
	if err != nil {
		return nil, fmt.Errorf("read pending dir: %w", err)
	}

	var facts []OutboxFact
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(o.pendingDir(), entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			log.Printf("outbox: skipping unreadable pending file %s: %v", path, err)
			continue
		}
		var f OutboxFact
		if err := json.Unmarshal(data, &f); err != nil {
			log.Printf("outbox: skipping corrupt pending file %s: %v", path, err)
			continue
		}
		facts = append(facts, f)
	}
	return facts, nil
}

// MarkDone moves a fact from pending to done. If the pending file is already
// gone the call is a no-op.
func (o *Outbox) MarkDone(factID string) error {
	if err := o.ensureDirs(); err != nil {
		return err
	}

	src := filepath.Join(o.pendingDir(), factID+".json")
	dst := filepath.Join(o.doneDir(), factID+".json")

	if err := os.Rename(src, dst); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("mark fact done: %w", err)
	}
	return nil
}
