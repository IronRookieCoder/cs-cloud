package membertask

import (
	"errors"
	"os"
	"testing"
)

func TestStoreKeepsOldIndexWhenReplaceFails(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := store.Save(Index{SchemaVersion: SchemaVersion, Revision: 1}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store.replace = func(string, string) error { return errors.New("crash") }
	if err := store.Save(Index{SchemaVersion: SchemaVersion, Revision: 2}); err == nil {
		t.Fatal("Save succeeded, want replace error")
	}
	got, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Revision != 1 {
		t.Fatalf("revision = %d, want 1", got.Revision)
	}
}

func TestStoreRejectsCorruptIndex(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := os.WriteFile(store.indexPath(), []byte(`{"schema_version":"2.0"}`), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}
	_, err = store.Load()
	te, ok := err.(*TaskError)
	if !ok || te.Code != "local_task_store_corrupt" {
		t.Fatalf("Load error = %#v", err)
	}
}

func TestStoreSavesAndListsTaskRecords(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	record := TaskRecord{Key: key, Activity: ActivityPrepared}
	if err := store.SaveTask(record); err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	got, found, err := store.LoadTask(key)
	if err != nil || !found || got.Key != key {
		t.Fatalf("LoadTask = %+v, %v, %v", got, found, err)
	}
	list, err := store.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
}
