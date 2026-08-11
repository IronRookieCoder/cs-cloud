package membertask

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestStartPauseUseOnlyLocalStore(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	record := TaskRecord{Key: key, Prepared: true, Activity: ActivityPrepared}
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil)
	started, err := svc.Start(context.Background(), key)
	if err != nil || started.Activity != ActivityActive || !started.Performed || started.Outcome != OutcomeCompleted {
		t.Fatalf("Start = %+v, %v", started, err)
	}
	repeatedStart, err := svc.Start(context.Background(), key)
	if err != nil || repeatedStart.Performed || repeatedStart.Outcome != OutcomeAlreadyCompleted {
		t.Fatalf("repeated Start = %+v, %v", repeatedStart, err)
	}
	paused, err := svc.Pause(context.Background(), key)
	if err != nil || paused.Activity != ActivityPaused || !paused.Performed || paused.Outcome != OutcomeCompleted {
		t.Fatalf("Pause = %+v, %v", paused, err)
	}
	repeatedPause, err := svc.Pause(context.Background(), key)
	if err != nil || repeatedPause.Performed || repeatedPause.Outcome != OutcomeAlreadyCompleted {
		t.Fatalf("repeated Pause = %+v, %v", repeatedPause, err)
	}
}

func TestStartRejectsUnpreparedTask(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	_ = store.SaveTask(TaskRecord{Key: key})
	_, err := NewService(store, nil).Start(context.Background(), key)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_task_state" {
		t.Fatalf("Start error = %#v", err)
	}
}

func TestDirtyRefreshTracksWritableOutput(t *testing.T) {
	root := t.TempDir()
	store, _ := OpenStore(root)
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	path := filepath.Join(dir, "output", "result.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("draft"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := BuildManifest(dir, []MaterialSource{{Identity: "result", SourceVersion: "v1", RelativePath: "output/result.md", Role: MaterialOutputWritable}})
	if err := store.SaveTask(TaskRecord{Key: key, Directory: dir, Prepared: true, Activity: ActivityActive, Manifest: &manifest}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("done"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshed, err := NewService(store, nil).Refresh(context.Background(), key)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if !refreshed.Dirty || len(refreshed.DirtyPaths) != 1 || refreshed.DirtyPaths[0] != "output/result.md" {
		t.Fatalf("record = %+v", refreshed)
	}
}

func TestCriticRefreshRejectsLocalMaterialChanges(t *testing.T) {
	root := t.TempDir()
	store, _ := OpenStore(root)
	key, _ := ParseTaskKey("cloud/ws/node/critic")
	dir := store.Layout().TaskDir(key)
	path := filepath.Join(dir, "review", "submission.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("submitted"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, _ := BuildManifest(dir, []MaterialSource{{Identity: "submission", SourceVersion: "v1", RelativePath: "review/submission.md", Role: MaterialReferenceOnly}})
	_ = store.SaveTask(TaskRecord{Key: key, Directory: dir, Prepared: true, Activity: ActivityActive, Manifest: &manifest})
	_ = os.Chmod(path, 0o600)
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewService(store, nil).Refresh(context.Background(), key)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "input_material_modified" {
		t.Fatalf("Refresh error = %#v", err)
	}
}
