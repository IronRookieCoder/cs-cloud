package membertask

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
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

type deleteReceipt struct {
	ID        string    `json:"id"`
	Key       TaskKey   `json:"key"`
	ExpiresAt time.Time `json:"expires_at"`
}

var deletePreviewIDPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

func (s *Service) PreviewDelete(ctx context.Context, key TaskKey, mode DeleteMode) (DeletePreview, error) {
	unlock := s.lockTask(key)
	defer unlock()

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
	record, err = refreshDeleteFacts(record)
	if err != nil {
		return DeletePreview{}, err
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
	unlock := s.lockTask(key)
	defer unlock()

	_ = ctx
	record, found, err := s.store.LoadTask(key)
	if err != nil {
		return err
	}
	if !found {
		completed, err := s.loadDeleteReceipt(key, previewID)
		if err != nil {
			return err
		}
		if completed {
			return nil
		}
		return newTaskError("preview_stale", "delete preview does not match local task", nil)
	}
	if record.DeletePreview == nil || record.DeletePreview.ID != previewID {
		return newTaskError("preview_stale", "delete preview does not match local task", nil)
	}
	preview := *record.DeletePreview
	if !s.now().Before(preview.ExpiresAt) {
		return newTaskError("preview_stale", "delete preview has expired", nil)
	}
	if operationBlocksDelete(record.Operation) {
		return newTaskError("operation_result_unknown", "accepted operation must be recovered before deletion", nil)
	}
	record, err = refreshDeleteFacts(record)
	if err != nil {
		return err
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
	originalRoot := record.PreparationRoot
	if originalRoot == "" {
		originalRoot = s.store.Layout().TasksRoot()
	}
	quarantineRoot := s.store.Layout().QuarantineRoot()
	if record.PreparationRoot != "" && !samePath(record.PreparationRoot, s.store.Layout().TasksRoot()) {
		quarantineRoot = filepath.Join(record.PreparationRoot, ".quarantine")
	}
	journal := DeleteJournal{
		ID: preview.ID, Key: key, Phase: DeletePhaseDeleting, Original: record.Directory,
		OriginalRoot: originalRoot, Quarantine: filepath.Join(quarantineRoot, preview.ID), QuarantineRoot: quarantineRoot, CreatedAt: s.now().UTC(),
	}
	if err := s.saveDeleteJournal(journal); err != nil {
		return err
	}
	return s.continueDelete(journal)
}

func refreshDeleteFacts(record TaskRecord) (TaskRecord, error) {
	if record.Manifest == nil || record.Directory == "" {
		return record, nil
	}
	verification, err := VerifyManifest(record.Directory, *record.Manifest)
	if err != nil {
		return TaskRecord{}, err
	}
	record.Dirty = verification.Dirty
	record.DirtyPaths = verification.ChangedPaths
	return record, nil
}

func (s *Service) RecoverDeleteJournals(ctx context.Context) error {
	root := s.store.Layout().DeleteJournalsRoot()
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return newTaskError("local_task_store_unavailable", "cannot list delete journals", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var recoveryErr error
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return appendRecoveryError(recoveryErr, err)
		}
		path := filepath.Join(root, entry.Name())
		if filepath.Ext(entry.Name()) == ".done" {
			if err := s.removeExpiredDeleteReceipt(path); err != nil {
				recoveryErr = appendRecoveryError(recoveryErr, err)
			}
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var journal DeleteJournal
		if err := s.store.readJSON(path, &journal); err != nil {
			recoveryErr = appendRecoveryError(recoveryErr, newTaskError("local_task_store_corrupt", "delete journal is invalid", err))
			recoveryErr = appendRecoveryError(recoveryErr, quarantineCorruptJournal(path))
			continue
		}
		unlock := s.lockTask(journal.Key)
		err := s.continueDelete(journal)
		unlock()
		if err != nil {
			recoveryErr = appendRecoveryError(recoveryErr, err)
		}
	}
	return recoveryErr
}

func quarantineCorruptJournal(path string) error {
	for suffix := 0; ; suffix++ {
		target := path + ".corrupt"
		if suffix > 0 {
			target += "." + strconv.Itoa(suffix)
		}
		if _, err := os.Stat(target); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return newTaskError("local_task_store_unavailable", "cannot inspect corrupt journal quarantine", err)
		}
		if err := os.Rename(path, target); err != nil {
			return newTaskError("local_task_store_unavailable", "cannot quarantine corrupt journal", err)
		}
		return nil
	}
}

func appendRecoveryError(current, next error) error {
	if next == nil {
		return current
	}
	if current == nil {
		return next
	}
	return errors.Join(current, next)
}

func (s *Service) continueDelete(journal DeleteJournal) error {
	if err := validateJournalID(journal.ID); err != nil {
		return err
	}
	originalRoot := journal.OriginalRoot
	if originalRoot == "" {
		originalRoot = s.store.Layout().TasksRoot()
	}
	quarantineRoot := journal.QuarantineRoot
	if quarantineRoot == "" {
		quarantineRoot = s.store.Layout().QuarantineRoot()
	}
	if journal.Original != "" {
		if err := ValidateContainedPath(originalRoot, journal.Original); err != nil {
			return err
		}
	}
	if err := ValidateContainedPath(quarantineRoot, journal.Quarantine); err != nil {
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
		if deletePreviewIDPattern.MatchString(journal.ID) {
			if err := s.saveDeleteReceipt(deleteReceipt{ID: journal.ID, Key: journal.Key, ExpiresAt: s.now().UTC().Add(24 * time.Hour)}); err != nil {
				return err
			}
		}
		return s.removeDeleteJournal(journal.ID)
	}
	return nil
}

func (s *Service) saveDeleteReceipt(receipt deleteReceipt) error {
	if !deletePreviewIDPattern.MatchString(receipt.ID) {
		return newTaskError("local_task_store_corrupt", "delete receipt identity is invalid", nil)
	}
	if _, err := ParseTaskKey(receipt.Key.String()); err != nil {
		return newTaskError("local_task_store_corrupt", "delete receipt task identity is invalid", err)
	}
	if err := os.MkdirAll(s.store.Layout().DeleteJournalsRoot(), 0o700); err != nil {
		return newTaskError("local_task_store_unavailable", "cannot create delete receipt directory", err)
	}
	return s.store.writeJSON(filepath.Join(s.store.Layout().DeleteJournalsRoot(), receipt.ID+".done"), receipt)
}

func (s *Service) loadDeleteReceipt(key TaskKey, previewID string) (bool, error) {
	if !deletePreviewIDPattern.MatchString(previewID) {
		return false, nil
	}
	path := filepath.Join(s.store.Layout().DeleteJournalsRoot(), previewID+".done")
	var receipt deleteReceipt
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || json.Unmarshal(b, &receipt) != nil {
		return false, newTaskError("local_task_store_corrupt", "delete receipt is invalid", err)
	}
	if receipt.ID != previewID || receipt.Key != key {
		return false, newTaskError("local_task_store_corrupt", "delete receipt identity does not match", nil)
	}
	if !s.now().Before(receipt.ExpiresAt) {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, newTaskError("local_task_store_unavailable", "cannot remove expired delete receipt", err)
		}
		return false, nil
	}
	return true, nil
}

func (s *Service) removeExpiredDeleteReceipt(path string) error {
	var receipt deleteReceipt
	b, err := os.ReadFile(path)
	if err != nil {
		return newTaskError("local_task_store_unavailable", "cannot read delete receipt", err)
	}
	if err := json.Unmarshal(b, &receipt); err != nil {
		return newTaskError("local_task_store_corrupt", "delete receipt is invalid", err)
	}
	filenameID := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if receipt.ID != filenameID || !deletePreviewIDPattern.MatchString(receipt.ID) {
		return newTaskError("local_task_store_corrupt", "delete receipt identity does not match", nil)
	}
	if _, err := ParseTaskKey(receipt.Key.String()); err != nil {
		return newTaskError("local_task_store_corrupt", "delete receipt task identity is invalid", err)
	}
	if s.now().Before(receipt.ExpiresAt) {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return newTaskError("local_task_store_unavailable", "cannot remove expired delete receipt", err)
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
	if err := validateJournalID(journal.ID); err != nil {
		return err
	}
	if err := os.MkdirAll(s.store.Layout().DeleteJournalsRoot(), 0o700); err != nil {
		return newTaskError("local_task_store_unavailable", "cannot create delete journal directory", err)
	}
	return s.store.writeJSON(filepath.Join(s.store.Layout().DeleteJournalsRoot(), journal.ID+".json"), journal)
}

func (s *Service) removeDeleteJournal(id string) error {
	if err := validateJournalID(id); err != nil {
		return err
	}
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
