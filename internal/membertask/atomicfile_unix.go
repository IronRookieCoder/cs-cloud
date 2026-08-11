//go:build !windows

package membertask

import "os"

func atomicReplace(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}
