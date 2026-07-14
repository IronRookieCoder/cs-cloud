package cli

import (
	"fmt"
	"path/filepath"

	"cs-cloud/internal/app"
	"cs-cloud/internal/localserver"
)

// gc removes expired attachments from the local cache and reports freed
// bytes. Operates directly on the daemon's storage directory, so it works
// even when the daemon isn't running.
func gc(a *app.App) error {
	printTitle("cs-cloud gc")

	dir := filepath.Join(a.RootDir(), "attachments")
	if a.RootDir() == "" {
		printError("root dir not resolved")
		return fmt.Errorf("root dir not configured")
	}

	deleted, freedBytes, err := localserver.GcExpiredAttachments(dir)
	if err != nil {
		printError("gc failed: %v", err)
		return err
	}

	printSuccess("removed %d expired attachment(s)", deleted)
	printKV("freed", formatBytes(freedBytes))
	printKV("storage", dir)
	return nil
}

// formatBytes renders a byte count as a human-readable string (B/KB/MB/GB).
// Uses 1024-based binary units to match the attachment upload size budget.
func formatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"KB", "MB", "GB", "TB"}[exp]
	return fmt.Sprintf("%.2f %s", float64(n)/float64(div), suffix)
}
