//go:build !windows

package cli

import "os"

func openNullDevice() (*os.File, error) {
	return os.OpenFile(os.DevNull, os.O_RDWR, 0)
}
