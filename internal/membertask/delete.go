package membertask

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type DeleteMode string

const (
	DeleteModeNormal       DeleteMode = "delete"
	DeleteModeForceDiscard DeleteMode = "force_discard"
)

type DeletePreview struct {
	ID                string     `json:"id"`
	Mode              DeleteMode `json:"mode"`
	RiskDigest        string     `json:"risk_digest"`
	Risks             []string   `json:"risks,omitempty"`
	DirectoryIdentity string     `json:"directory_identity"`
	RequiresForce     bool       `json:"requires_force"`
	ExpiresAt         time.Time  `json:"expires_at"`
}

func (s *Service) PreviewDelete(ctx context.Context, key TaskKey, mode DeleteMode) (DeletePreview, error) {
	_ = ctx
	if mode != DeleteModeNormal && mode != DeleteModeForceDiscard {
		return DeletePreview{}, newTaskError("invalid_arguments", "delete mode is invalid", nil)
	}
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return DeletePreview{}, err
	}
	if !found {
		return DeletePreview{}, newTaskError("task_not_found", "local task does not exist", nil)
	}
	if operationBlocksDelete(record.Operation) {
		return DeletePreview{}, newTaskError("operation_result_unknown", "accepted operation must be recovered before deletion", nil)
	}
	risks := deleteRisks(record, s.now())
	requiresForce := len(risks) > 0
	riskDigest := digestJSON(struct {
		Directory string
		Risks     []string
	}{directoryIdentity(record), risks})
	preview := DeletePreview{
		Mode: mode, RiskDigest: riskDigest, Risks: risks, DirectoryIdentity: directoryIdentity(record), RequiresForce: requiresForce,
		ExpiresAt: s.now().UTC().Add(15 * time.Minute),
	}
	preview.ID = digestJSON(struct {
		Key, Risk, Mode, Expiry string
	}{key.String(), riskDigest, string(mode), preview.ExpiresAt.Format(time.RFC3339Nano)})[:24]
	record.DeletePreview = &preview
	if err := s.store.SaveTask(record); err != nil {
		return DeletePreview{}, err
	}
	return preview, nil
}

func (s *Service) ConfirmDelete(ctx context.Context, key TaskKey, previewID string) error {
	_ = ctx
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return err
	}
	if !found || record.DeletePreview == nil || record.DeletePreview.ID != previewID {
		return newTaskError("preview_stale", "delete preview does not match local task", nil)
	}
	preview := *record.DeletePreview
	if !s.now().Before(preview.ExpiresAt) {
		return newTaskError("preview_stale", "delete preview has expired", nil)
	}
	if operationBlocksDelete(record.Operation) {
		return newTaskError("operation_result_unknown", "accepted operation must be recovered before deletion", nil)
	}
	risks := deleteRisks(record, s.now())
	riskDigest := digestJSON(struct {
		Directory string
		Risks     []string
	}{directoryIdentity(record), risks})
	if riskDigest != preview.RiskDigest || directoryIdentity(record) != preview.DirectoryIdentity {
		return newTaskError("preview_stale", "delete risks changed after preview", nil)
	}
	if len(risks) > 0 && preview.Mode != DeleteModeForceDiscard {
		return newTaskError("force_discard_required", "task contains local or recoverable state", nil)
	}
	journal := DeleteJournal{
		ID: preview.ID, Key: key, Phase: DeletePhaseDeleting, Original: record.Directory,
		Quarantine: filepath.Join(s.store.Layout().QuarantineRoot(), preview.ID), CreatedAt: s.now().UTC(),
	}
	if err := s.saveDeleteJournal(journal); err != nil {
		return err
	}
	return s.continueDelete(journal)
}

func (s *Service) RecoverDeleteJournals(ctx context.Context) error {
	_ = ctx
	root := s.store.Layout().DeleteJournalsRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newTaskError("local_task_store_unavailable", "cannot list delete journals", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var journal DeleteJournal
		if err := s.store.readJSON(filepath.Join(root, entry.Name()), &journal); err != nil {
			return newTaskError("local_task_store_corrupt", "delete journal is invalid", err)
		}
		if err := s.continueDelete(journal); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) continueDelete(journal DeleteJournal) error {
	if journal.Original != "" {
		if err := ValidateContainedPath(s.store.Layout().TasksRoot(), journal.Original); err != nil {
			return err
		}
	}
	if err := ValidateContainedPath(s.store.Layout().QuarantineRoot(), journal.Quarantine); err != nil {
		return err
	}
	originalExists := journal.Original != "" && pathExists(journal.Original)
	quarantineExists := pathExists(journal.Quarantine)
	if originalExists && quarantineExists {
		return s.deleteInconsistent(journal.Key, "delete has both original and quarantine directories")
	}

	if journal.Phase == DeletePhaseDeleting {
		if originalExists {
			if err := os.MkdirAll(filepath.Dir(journal.Quarantine), 0o700); err != nil {
				return err
			}
			if err := os.Rename(journal.Original, journal.Quarantine); err != nil {
				return newTaskError("delete_recovery_required", "cannot quarantine task directory", err)
			}
			quarantineExists = true
		} else if journal.Original != "" && !quarantineExists {
			return s.deleteInconsistent(journal.Key, "delete source directory disappeared")
		}
		journal.Phase = DeletePhaseQuarantined
		if err := s.saveDeleteJournal(journal); err != nil {
			return err
		}
	}

	if journal.Phase == DeletePhaseQuarantined {
		if journal.Original != "" && !quarantineExists {
			return s.deleteInconsistent(journal.Key, "quarantined task directory is missing")
		}
		if err := s.store.RemoveTask(journal.Key); err != nil {
			return err
		}
		journal.Phase = DeletePhaseUnindexed
		if err := s.saveDeleteJournal(journal); err != nil {
			return err
		}
	}

	if journal.Phase == DeletePhaseUnindexed {
		if quarantineExists {
			makeTreeWritable(journal.Quarantine)
			if err := os.RemoveAll(journal.Quarantine); err != nil {
				return newTaskError("delete_recovery_required", "cannot remove quarantined task directory", err)
			}
		}
		journal.Phase = DeletePhaseCompleted
		if err := s.saveDeleteJournal(journal); err != nil {
			return err
		}
	}
	if journal.Phase == DeletePhaseCompleted {
		return s.removeDeleteJournal(journal.ID)
	}
	return nil
}

func deleteRisks(record TaskRecord, now time.Time) []string {
	var risks []string
	if record.Dirty {
		risks = append(risks, "dirty")
	}
	if record.Preview != nil && (record.Preview.ExpiresAt.IsZero() || now.Before(record.Preview.ExpiresAt)) {
		risks = append(risks, "active_preview")
	}
	if record.SyncPending {
		risks = append(risks, "sync_pending")
	}
	if record.Operation != nil && record.Operation.Status != OperationCompleted {
		risks = append(risks, "operation_recovery_required")
	}
	if record.CleanupPending {
		risks = append(risks, "cleanup_pending")
	}
	sort.Strings(risks)
	return risks
}

func operationBlocksDelete(operation *Operation) bool {
	if operation == nil {
		return false
	}
	switch operation.Status {
	case OperationAccepted, OperationRunning, OperationUnknown:
		return true
	default:
		return false
	}
}

func directoryIdentity(record TaskRecord) string {
	manifestDigest := ""
	if record.Manifest != nil {
		manifestDigest = record.Manifest.MaterialDigest
	}
	return digestJSON(struct{ Key, Directory, Manifest string }{record.Key.String(), record.Directory, manifestDigest})
}

func (s *Service) saveDeleteJournal(journal DeleteJournal) error {
	if err := os.MkdirAll(s.store.Layout().DeleteJournalsRoot(), 0o700); err != nil {
		return newTaskError("local_task_store_unavailable", "cannot create delete journal directory", err)
	}
	return s.store.writeJSON(filepath.Join(s.store.Layout().DeleteJournalsRoot(), journal.ID+".json"), journal)
}

func (s *Service) removeDeleteJournal(id string) error {
	err := os.Remove(filepath.Join(s.store.Layout().DeleteJournalsRoot(), id+".json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return newTaskError("local_task_store_unavailable", "cannot remove delete journal", err)
	}
	return nil
}

func (s *Service) deleteInconsistent(key TaskKey, message string) error {
	if record, found, err := s.store.LoadTask(key); err == nil && found {
		record.CleanupPending = true
		_ = s.store.SaveTask(record)
	}
	return newTaskError("delete_recovery_required", message, nil)
}

func makeTreeWritable(root string) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil {
			if info.IsDir() {
				_ = os.Chmod(path, 0o700)
			} else {
				_ = os.Chmod(path, 0o600)
			}
		}
		return nil
	})
}
