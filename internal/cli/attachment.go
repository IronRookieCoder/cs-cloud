package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"cs-cloud/internal/app"
	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/provider"
)

// attachmentCmd implements `cs-cloud attachment <subcommand>`.
func attachmentCmd(a *app.App, args []string) error {
	_ = a
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud attachment download <attachment-id> [--output <path>]")
	}
	switch args[0] {
	case "download":
		return attachmentDownload(a, args[1:])
	default:
		return fmt.Errorf("unknown attachment command: %s", args[0])
	}
}

// attachmentDownload: `cs-cloud attachment download <id> [--output <path>]`
// Fetches attachment metadata (which includes a signed download_url), then
// downloads the file content.
func attachmentDownload(a *app.App, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud attachment download <attachment-id> [--output <path>]")
	}
	attachmentID := args[0]
	var outputPath string
	for i := 1; i < len(args); i++ {
		if args[i] == "--output" && i+1 < len(args) {
			outputPath = args[i+1]
			i++
		}
	}

	cfg := a.Config()
	creds, err := a.Credentials()
	if err != nil {
		return err
	}
	client := workflowagent.NewClient(cfg.Workflow.MulticaBaseURL, "", func() (*provider.Credentials, error) {
		return creds, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	att, err := client.GetAttachment(ctx, attachmentID)
	if err != nil {
		return fmt.Errorf("get attachment: %w", err)
	}
	if att.DownloadURL == "" {
		return fmt.Errorf("attachment %s has no download URL", attachmentID)
	}

	if outputPath == "" {
		outputPath = filepath.Base(att.Filename)
		if outputPath == "" || outputPath == "." || outputPath == "/" {
			outputPath = "attachment-" + attachmentID[:min(8, len(attachmentID))]
		}
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, att.DownloadURL, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download: status %d", resp.StatusCode)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		return err
	}
	fmt.Printf("downloaded %s (%d bytes) → %s\n", att.Filename, n, outputPath)
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
