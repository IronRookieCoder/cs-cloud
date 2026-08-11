//go:build !windows

package membertask

import (
	"errors"
	"os"
)

func atomicReplace(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func syncParentDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}
