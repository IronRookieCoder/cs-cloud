package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/workflowrunner"
)

// taskCmd implements `cs-cloud workflow task`, the in-task tooling an agent uses
// to explicitly signal task completion (worker) or a review decision (critic).
// It calls back into this device's localserver, which signals the driver.
func taskCmd(a *app.App, args []string) error {
	_ = a // task-context command; uses task env, not daemon config/credentials
	if len(args) == 0 {
		printTaskUsage()
		return nil
	}
	switch args[0] {
	case "complete":
		return runTaskComplete(args[1:])
	case "review":
		return runTaskReview(args[1:])
	case "help", "-h", "--help":
		printTaskUsage()
		return nil
	default:
		printTaskUsage()
		return fmt.Errorf("unknown workflow task command: %s", args[0])
	}
}

func printTaskUsage() {
	printTitle("cs-cloud workflow task")
	printSection("Usage")
	fmt.Println(dimStyle.Render("  cs-cloud workflow task <action> [flags]"))
	printSection("Actions")
	cmds := [][2]string{
		{"complete", "Signal task completion (worker). --summary <text>. Use as your LAST action."},
		{"review", "Signal a review decision (critic). --decision approve|reject [--reason <text>]"},
	}
	fmt.Print(renderKV(cmds))
}

// runTaskComplete signals that the worker agent has finished its work. It must
// be the agent's last action: until it is called, the driver holds the task
// open (idle is not completion).
func runTaskComplete(args []string) error {
	summary, _ := parseStringFlag(args, "--summary")
	return postTaskCompletion(map[string]string{
		"action":  "complete",
		"summary": summary,
	})
}

// runTaskReview signals a critic's approve/reject decision.
func runTaskReview(args []string) error {
	decision, _ := parseStringFlag(args, "--decision")
	if decision != "approve" && decision != "reject" {
		return fmt.Errorf("--decision must be approve or reject, got %q", decision)
	}
	reason, _ := parseStringFlag(args, "--reason")
	return postTaskCompletion(map[string]string{
		"action":   "review",
		"decision": decision,
		"reason":   reason,
	})
}

// postTaskCompletion POSTs the completion payload to this device's localserver
// endpoint, which forwards it to the driver via SignalTaskCompletion.
func postTaskCompletion(body map[string]string) error {
	base := os.Getenv(workflowrunner.EnvLocalServerURL)
	taskID := os.Getenv(workflowrunner.EnvTaskID)
	if base == "" {
		return fmt.Errorf("%s not set (this command must run inside a workflow task)", workflowrunner.EnvLocalServerURL)
	}
	if taskID == "" {
		return fmt.Errorf("%s not set (this command must run inside a workflow task)", workflowrunner.EnvTaskID)
	}
	endpoint := strings.TrimRight(base, "/") + "/api/v1/workflow/tasks/" + taskID + "/complete"

	payload, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("complete request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("complete: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// parseStringFlag reads `--name <value>` from a flat arg slice. Returns the
// value and whether the flag was present.
func parseStringFlag(args []string, name string) (string, bool) {
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", true
		}
	}
	return "", false
}
