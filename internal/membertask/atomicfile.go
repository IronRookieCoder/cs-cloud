package membertask

import (
	"fmt"
	"os"
	"path/filepath"
)

type replaceFunc func(oldPath, newPath string) error

var syncDirectory = syncParentDirectory

func atomicWriteFile(path string, data []byte, replace replaceFunc) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replace(tmpPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("sync parent directory: %w", err)
	}
	return nil
}
