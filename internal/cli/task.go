package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/workflowrunner"
)

// taskCompleteAttempts caps how many times the complete signal is retried. The
// call is the agent's only chance to signal completion, so transient failures
// (connection blip, 5xx, driver briefly unavailable) are retried rather than
// surfaced as fatal on the first try.
const taskCompleteAttempts = 3

// taskCompleteRetryWait paces retries. Short: completion is latency-sensitive
// because the signal must reach the localserver and enter the driver's
// completion registry before execute returns and unregisterCompletion removes
// the task's entry - after that the POST gets a 409 and the explicit
// completion payload is lost. (The fail/complete race is arbitrated
// server-side via ApplyTaskFact's FailureGraceWindow; this retry only guards
// device-side signal delivery.)
const taskCompleteRetryWait = 500 * time.Millisecond

// errTaskAlreadyFinished represents a 409 from localserver: the task is no
// longer running (first complete succeeded, or it timed out / was finalized).
// Retrying cannot change that, so the caller treats it as accepted instead of
// trapping the agent in a "task not running" retry loop.
var errTaskAlreadyFinished = errors.New("task already finished")

// taskCmd implements `cs-cloud workflow task`, the in-task tooling an agent uses
// to explicitly signal task completion (worker) or a review decision (critic).
// It calls back into this device's localserver, which signals the driver.
//
// The agent passes NO context flags: task id + local server URL come from the
// environment, which `cs-cloud workflow` fills from .cs-cloud.env when env
// propagation through the agent subprocess failed. Each command takes at most
// one optional flag (summary / reason) to keep the surface small and hard to
// misuse.
func taskCmd(a *app.App, args []string) error {
	_ = a // task-context command; uses task env, not daemon config/credentials
	if len(args) == 0 {
		printTaskUsage()
		return nil
	}
	switch args[0] {
	case "complete":
		return runTaskComplete(args[1:])
	case "approve":
		return runTaskReview(args[1:], "approve")
	case "reject":
		return runTaskReview(args[1:], "reject")
	case "help", "-h", "--help":
		printTaskUsage()
		return nil
	default:
		fmt.Println("Unknown workflow task command: " + args[0] + ". Valid actions: complete, approve, reject.")
		return nil
	}
}

func printTaskUsage() {
	printTitle("cs-cloud workflow task")
	printSection("Usage")
	fmt.Println(dimStyle.Render("  cs-cloud workflow task <action> [flags]"))
	printSection("Actions")
	cmds := [][2]string{
		{"complete", "Worker finished. [--summary <text>]"},
		{"approve", "Critic approves. [--reason <text>]"},
		{"reject", "Critic requests rework. [--reason <text>]"},
	}
	fmt.Print(renderKV(cmds))
}

// completionMessage renders the result of a completion/review POST as
// agent-facing text. The task CLI never surfaces a non-zero exit to the agent:
// on success it confirms delivery, on failure it returns this guidance instead,
// so the agent reads the text and recovers (cd to the task root and retry)
// rather than being derailed by an error exit code. The task id is supplied to
// the agent separately in the task prompt (injectTaskEnvGuidance).
func completionMessage(subject string, err error) string {
	if err == nil {
		return subject + " delivered. You may stop."
	}
	return strings.ToUpper(subject) + " NOT DELIVERED: " + err.Error() + "\n" +
		"This is not a final failure. You are likely not in the task root, or the task server was briefly unavailable. cd to your task root and retry the command. If it still fails after a few attempts, report this message verbatim."
}

// runTaskComplete signals that the worker agent has finished its work. It must
// be the agent's last action: until it is called, the driver holds the task
// open (idle is not completion).
func runTaskComplete(args []string) error {
	summary, _ := parseStringFlag(args, "--summary")
	err := postTaskCompletion(map[string]string{
		"action":  "complete",
		"summary": summary,
	})
	fmt.Println(completionMessage("Task completion", err))
	return nil
}

// runTaskReview signals a critic's decision (approve or reject).
func runTaskReview(args []string, decision string) error {
	reason, _ := parseStringFlag(args, "--reason")
	err := postTaskCompletion(map[string]string{
		"action":   "review",
		"decision": decision,
		"reason":   reason,
	})
	fmt.Println(completionMessage("Review decision", err))
	return nil
}

// postTaskCompletion POSTs the completion payload to this device's localserver
// endpoint, which forwards it to the driver via SignalTaskCompletion. Task id +
// local URL come from the env (filled from .cs-cloud.env by loadTaskEnvFile when
// env propagation through the agent subprocess failed).
//
// The call is the agent's only chance to signal completion, so transient
// failures are retried (taskCompleteAttempts). A 409 means the task already
// finished (first complete succeeded, or it timed out) and is treated as
// accepted — otherwise the agent loops on "task not running" until agent_timeout.
func postTaskCompletion(body map[string]string) error {
	localURL := os.Getenv(workflowrunner.EnvLocalServerURL)
	taskID := os.Getenv(workflowrunner.EnvTaskID)
	if localURL == "" {
		return fmt.Errorf("%s not set (run inside a workflow task; context is in .cs-cloud.env)", workflowrunner.EnvLocalServerURL)
	}
	if taskID == "" {
		return fmt.Errorf("%s not set (run inside a workflow task; context is in .cs-cloud.env)", workflowrunner.EnvTaskID)
	}
	endpoint := strings.TrimRight(localURL, "/") + "/api/v1/workflow/tasks/" + taskID + "/complete"
	payload, _ := json.Marshal(body)

	var lastErr error
	for attempt := 1; attempt <= taskCompleteAttempts; attempt++ {
		err := postCompletionOnce(endpoint, payload)
		if err == nil || errors.Is(err, errTaskAlreadyFinished) {
			return nil
		}
		lastErr = err
		if attempt < taskCompleteAttempts {
			time.Sleep(taskCompleteRetryWait)
		}
	}
	return fmt.Errorf("complete (after %d attempts): %w", taskCompleteAttempts, lastErr)
}

// postCompletionOnce performs a single complete POST and maps the response:
// 2xx → nil, 409 → errTaskAlreadyFinished (treated as accepted upstream),
// anything else → a retryable error.
func postCompletionOnce(endpoint string, payload []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// Authenticate to the localserver's apiAuth middleware when an API key is
	// configured (no-op when none — the middleware is then a pass-through).
	if key := os.Getenv(workflowrunner.EnvLocalServerAPIKey); key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return errTaskAlreadyFinished
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("complete: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
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
