package membertask

import (
	"errors"
	"path/filepath"
	"testing"
)

var errDirectorySyncFailed = errors.New("directory sync failed")

func TestAtomicWriteFilePropagatesParentSyncError(t *testing.T) {
	previous := syncDirectory
	t.Cleanup(func() { syncDirectory = previous })
	syncDirectory = func(string) error { return errDirectorySyncFailed }

	err := atomicWriteFile(filepath.Join(t.TempDir(), "index.json"), []byte("data"), atomicReplace)
	if err == nil || !errors.Is(err, errDirectorySyncFailed) {
		t.Fatalf("atomicWriteFile error = %v", err)
	}
}
