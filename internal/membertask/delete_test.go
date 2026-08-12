package membertask

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeleteCleanEndedTaskWithBoundPreview(t *testing.T) {
	store, key, dir := seedDeleteTask(t, TaskRecord{Ended: true})
	svc := NewService(store, nil)
	preview, err := svc.PreviewDelete(context.Background(), key, DeleteModeNormal)
	if err != nil {
		t.Fatalf("PreviewDelete: %v", err)
	}
	if preview.RequiresForce {
		t.Fatalf("preview = %+v", preview)
	}
	if err := svc.ConfirmDelete(context.Background(), key, preview.ID); err != nil {
		t.Fatalf("ConfirmDelete: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("task directory remains: %v", err)
	}
	if _, found, _ := store.LoadTask(key); found {
		t.Fatal("task index record remains")
	}
}

func TestDeleteTaskPreparedInCustomWorkDir(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"result","kind":"file","source_version":"v1","relative_path":"output/result.md","role":"output_writable","content":"draft"}]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	transition, err := svc.Handle(context.Background(), key, PrepareOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := svc.PreviewDelete(context.Background(), key, DeleteModeForceDiscard)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmDelete(context.Background(), key, preview.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(transition.Directory); !os.IsNotExist(err) {
		t.Fatalf("task directory still exists: %v", err)
	}
	if _, found, err := store.LoadTask(key); err != nil || found {
		t.Fatalf("task record found=%t err=%v", found, err)
	}
	entries, err := filepath.Glob(filepath.Join(transition.PreparationRoot, ".quarantine", "*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("custom quarantine not cleaned: %v, err=%v", entries, err)
	}
}

func TestConfirmDeleteIsIdempotentForCompletedPreview(t *testing.T) {
	store, key, _ := seedDeleteTask(t, TaskRecord{Ended: true})
	svc := NewService(store, nil)
	preview, err := svc.PreviewDelete(context.Background(), key, DeleteModeForceDiscard)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ConfirmDelete(context.Background(), key, preview.ID); err != nil {
		t.Fatalf("first ConfirmDelete: %v", err)
	}
	if err := svc.ConfirmDelete(context.Background(), key, preview.ID); err != nil {
		t.Fatalf("repeated ConfirmDelete: %v", err)
	}
}

func TestDeleteDirtyTaskRequiresForceDiscardPreview(t *testing.T) {
	store, key, _ := seedDeleteTask(t, TaskRecord{Dirty: true, DirtyPaths: []string{"output/result.md"}})
	svc := NewService(store, nil)
	_, err := svc.PreviewDelete(context.Background(), key, DeleteModeNormal)
	taskErr, ok := err.(*TaskError)
	if !ok || taskErr.Code != "force_discard_required" {
		t.Fatalf("PreviewDelete error = %#v, want force_discard_required", err)
	}
	record, found, err := store.LoadTask(key)
	if err != nil || !found {
		t.Fatalf("LoadTask: found=%t err=%v", found, err)
	}
	if record.DeletePreview != nil {
		t.Fatalf("normal delete confirmation was stored: %+v", record.DeletePreview)
	}
	force, err := svc.PreviewDelete(context.Background(), key, DeleteModeForceDiscard)
	if err != nil {
		t.Fatalf("force PreviewDelete: %v", err)
	}
	if err := svc.ConfirmDelete(context.Background(), key, force.ID); err != nil {
		t.Fatalf("force ConfirmDelete: %v", err)
	}
}

func TestDeletePreviewRechecksWorkspaceForUnrecordedChanges(t *testing.T) {
	store, key, dir := seedDeleteTask(t, TaskRecord{Ended: true})
	resultPath := filepath.Join(dir, "result.txt")
	manifest, err := BuildManifest(dir, []MaterialSource{{
		Identity: "result", SourceVersion: "v1", RelativePath: "result.txt", Role: MaterialOutputWritable,
	}})
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	record.Manifest = &manifest
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, []byte("edited after prepare"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err = NewService(store, nil).PreviewDelete(context.Background(), key, DeleteModeNormal)
	taskErr, ok := err.(*TaskError)
	if !ok || taskErr.Code != "force_discard_required" {
		t.Fatalf("PreviewDelete error = %#v, want force_discard_required", err)
	}
}

func TestDeleteConfirmRejectsPreviewAfterWorkspaceChanges(t *testing.T) {
	store, key, dir := seedDeleteTask(t, TaskRecord{Ended: true})
	resultPath := filepath.Join(dir, "result.txt")
	manifest, err := BuildManifest(dir, []MaterialSource{{
		Identity: "result", SourceVersion: "v1", RelativePath: "result.txt", Role: MaterialOutputWritable,
	}})
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := store.LoadTask(key)
	if err != nil {
		t.Fatal(err)
	}
	record.Manifest = &manifest
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}

	preview, err := NewService(store, nil).PreviewDelete(context.Background(), key, DeleteModeNormal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultPath, []byte("edited after preview"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = NewService(store, nil).ConfirmDelete(context.Background(), key, preview.ID)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "preview_stale" {
		t.Fatalf("ConfirmDelete error = %#v, want preview_stale", err)
	}
}

func TestDeleteBlocksUnknownExternalOperation(t *testing.T) {
	store, key, _ := seedDeleteTask(t, TaskRecord{Operation: &Operation{ID: "operation-1", Status: OperationUnknown}})
	_, err := NewService(store, nil).PreviewDelete(context.Background(), key, DeleteModeForceDiscard)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "operation_result_unknown" {
		t.Fatalf("error = %#v", err)
	}
}

func TestRecoverDeleteJournalContinuesQuarantinedDelete(t *testing.T) {
	store, key, dir := seedDeleteTask(t, TaskRecord{Ended: true})
	quarantine := filepath.Join(store.Layout().QuarantineRoot(), "delete-1")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, quarantine); err != nil {
		t.Fatal(err)
	}
	journal := DeleteJournal{ID: "delete-1", Key: key, Phase: DeletePhaseQuarantined, Original: dir, Quarantine: quarantine, CreatedAt: time.Now()}
	if err := os.MkdirAll(store.Layout().DeleteJournalsRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.writeJSON(filepath.Join(store.Layout().DeleteJournalsRoot(), journal.ID+".json"), journal); err != nil {
		t.Fatal(err)
	}
	if err := NewService(store, nil).RecoverDeleteJournals(context.Background()); err != nil {
		t.Fatalf("RecoverDeleteJournals: %v", err)
	}
	if _, err := os.Stat(quarantine); !os.IsNotExist(err) {
		t.Fatalf("quarantine remains: %v", err)
	}
	if _, found, _ := store.LoadTask(key); found {
		t.Fatal("task index record remains")
	}
}

func TestRecoverDeleteJournalsContinuesAfterCorruptEntry(t *testing.T) {
	store, key, dir := seedDeleteTask(t, TaskRecord{Ended: true})
	quarantine := filepath.Join(store.Layout().QuarantineRoot(), "delete-1")
	if err := os.MkdirAll(filepath.Dir(quarantine), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, quarantine); err != nil {
		t.Fatal(err)
	}
	root := store.Layout().DeleteJournalsRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "000-corrupt.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := DeleteJournal{ID: "delete-1", Key: key, Phase: DeletePhaseQuarantined, Original: dir, Quarantine: quarantine, CreatedAt: time.Now()}
	if err := store.writeJSON(filepath.Join(root, journal.ID+".json"), journal); err != nil {
		t.Fatal(err)
	}

	err := NewService(store, nil).RecoverDeleteJournals(context.Background())
	if err == nil {
		t.Fatal("RecoverDeleteJournals error = nil, want corrupt journal error")
	}
	if _, statErr := os.Stat(quarantine); !os.IsNotExist(statErr) {
		t.Fatalf("valid quarantine remains after corrupt journal: %v", statErr)
	}
	if _, found, loadErr := store.LoadTask(key); loadErr != nil || found {
		t.Fatalf("task record found=%t err=%v", found, loadErr)
	}
}

func TestRecoverDeleteJournalsPreservesExistingCorruptQuarantine(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	root := store.Layout().DeleteJournalsRoot()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "broken.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	existing := path + ".corrupt"
	if err := os.WriteFile(existing, []byte("previous"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := NewService(store, nil).RecoverDeleteJournals(context.Background()); err == nil {
		t.Fatal("RecoverDeleteJournals error = nil, want corrupt journal error")
	}
	content, err := os.ReadFile(existing)
	if err != nil || string(content) != "previous" {
		t.Fatalf("existing quarantine changed: content=%q err=%v", content, err)
	}
	if _, err := os.Stat(path + ".corrupt.1"); err != nil {
		t.Fatalf("new corrupt journal was not quarantined separately: %v", err)
	}
}

func TestRecoverDeleteJournalsRemovesExpiredReceipts(t *testing.T) {
	store, key, _ := seedDeleteTask(t, TaskRecord{Ended: true})
	svc := NewService(store, nil)
	svc.now = func() time.Time { return time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC) }
	receipt := deleteReceipt{ID: "0123456789abcdef01234567", Key: key, ExpiresAt: svc.now().Add(-time.Minute)}
	if err := svc.saveDeleteReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Layout().DeleteJournalsRoot(), receipt.ID+".done")

	if err := svc.RecoverDeleteJournals(context.Background()); err != nil {
		t.Fatalf("RecoverDeleteJournals: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired receipt remains: %v", err)
	}
}

func seedDeleteTask(t *testing.T, overrides TaskRecord) (*Store, TaskKey, string) {
	t.Helper()
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "result.txt"), []byte("result"), 0o600); err != nil {
		t.Fatal(err)
	}
	overrides.Key = key
	overrides.Directory = dir
	overrides.Prepared = true
	if err := store.SaveTask(overrides); err != nil {
		t.Fatal(err)
	}
	return store, key, dir
}
