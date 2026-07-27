package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"cs-cloud/internal/app"
)

// checkoutConfig parameterizes runRepoCheckout for testing.
type checkoutConfig struct {
	repoURL    string
	baseBranch string
}

// parseCheckoutArgs parses `cs-cloud repo checkout <url> [--base <branch>]`.
func parseCheckoutArgs(args []string) (checkoutConfig, error) {
	cfg := checkoutConfig{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--base", "-b":
			if i+1 >= len(args) {
				return cfg, fmt.Errorf("--base requires a value")
			}
			cfg.baseBranch = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return cfg, fmt.Errorf("unknown flag %q", args[i])
			}
			cfg.repoURL = args[i]
		}
	}
	if cfg.repoURL == "" {
		return cfg, fmt.Errorf("repo url required: usage: cs-cloud repo checkout <url> [--base <branch>]")
	}
	return cfg, nil
}

// runRepoCheckout POSTs the checkout request to the cs-cloud localserver and
// prints the returned worktree path to stdout. It reads CS_CLOUD_SERVER_URL and
// MULTICA_TASK_ID from env (both pushed by the task payload in buildEnv).
func runRepoCheckout(cfg checkoutConfig) error {
	serverURL := strings.TrimRight(strings.TrimSpace(os.Getenv("CS_CLOUD_SERVER_URL")), "/")
	if serverURL == "" {
		return fmt.Errorf("CS_CLOUD_SERVER_URL not set (not running inside a cs-cloud task?)")
	}
	taskID := strings.TrimSpace(os.Getenv("MULTICA_TASK_ID"))
	if taskID == "" {
		return fmt.Errorf("MULTICA_TASK_ID not set")
	}
	body, _ := json.Marshal(map[string]string{
		"task_id":     taskID,
		"repo_url":    cfg.repoURL,
		"base_branch": cfg.baseBranch,
	})
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Post(serverURL+"/api/v1/repo/checkout", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("checkout request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("checkout: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Path string `json:"path"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return fmt.Errorf("decode checkout response: %w", err)
	}
	if env.Data.Path == "" {
		return fmt.Errorf("checkout returned empty path")
	}
	fmt.Println(env.Data.Path)
	return nil
}

// repoCmd implements `cs-cloud repo <subcommand>`. It is a task-context
// command: like deliverableCmd, it ignores *app.App and reads task env
// (CS_CLOUD_SERVER_URL / MULTICA_TASK_ID) pushed by the agent runtime.
func repoCmd(a *app.App, args []string) error {
	_ = a // task-context command; uses task env, not daemon config
	if len(args) == 0 {
		fmt.Println("usage: cs-cloud repo checkout <url> [--base <branch>]")
		return nil
	}
	switch args[0] {
	case "checkout":
		cfg, err := parseCheckoutArgs(args[1:])
		if err != nil {
			return err
		}
		return runRepoCheckout(cfg)
	case "help", "-h", "--help":
		fmt.Println("usage: cs-cloud repo checkout <url> [--base <branch>]")
		return nil
	default:
		return fmt.Errorf("unknown repo command: %s", args[0])
	}
}
