package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflowrunner"
)

const maxTaskEnvFileBytes = 1 << 20

func workflowCmd(a *app.App, args []string) error {
	// The agent passes --task <id> (learned from the task prompt) so cs-cloud
	// locates .cs-cloud.env without relying on the process cwd. Pop it before
	// dispatch so it never reaches a subcommand.
	args, taskID := popTaskFlag(args)
	if taskID != "" {
		// Locate the task root by scanning .cs-cloud.env files for this task id.
		// Sound under rework/resume: the runner reuses a prior round's directory
		// but writes the CURRENT task id into .cs-cloud.env, so matching by
		// content (not by directory name) finds the real task root.
		if root := findTaskRootByTaskID(a.Config().Workflow.WorkspacesRoot, taskID); root != "" {
			loadTaskEnvFileFrom(root)
		} else {
			loadTaskEnvFile() // no match → fall back to cwd walk-up
		}
	} else {
		// Resolve task context from .cs-cloud.env in the cwd (the task workdir).
		// cs-cloud writes this file at task start; loading it here lets the
		// workflow CLIs work even when env propagation through the agent
		// subprocess fails and the agent didn't pass --task.
		loadTaskEnvFile()
	}
	if len(args) == 0 {
		printWorkflowUsage()
		return nil
	}

	switch args[0] {
	case "workspace":
		return workflowWorkspaceCmd(a, args[1:])
	case "project":
		return workflowProjectCmd(a, args[1:])
	case "deliverable":
		return deliverableCmd(a, args[1:])
	case "task":
		return taskCmd(a, args[1:])
	case "help", "-h", "--help":
		printWorkflowUsage()
		return nil
	default:
		printWorkflowUsage()
		return fmt.Errorf("unknown workflow command: %s", args[0])
	}
}

func printWorkflowUsage() {
	printTitle("cs-cloud workflow")
	printSection("Usage")
	fmt.Println(dimStyle.Render("  cs-cloud workflow <resource> <action>"))
	printSection("Resources")
	cmds := [][2]string{
		{"workspace", "List/get/sync workspaces"},
		{"project", "List projects"},
		{"deliverable", "Submit document deliverables to Gitea"},
		{"task", "Signal task completion / review decision"},
	}
	fmt.Print(renderKV(cmds))
}

func workflowProjectCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		return workflowProjectList(a)
	}
	switch args[0] {
	case "list":
		return workflowProjectList(a)
	default:
		return fmt.Errorf("unknown workflow project command: %s", args[0])
	}
}

func workflowProjectList(a *app.App) error {
	cfg := a.Config()
	creds, err := a.Credentials()
	if err != nil {
		return err
	}
	wsID := os.Getenv("CS_CLOUD_WORKSPACE_ID")
	if wsID == "" {
		return fmt.Errorf("CS_CLOUD_WORKSPACE_ID not set (run inside a workflow task; context is in .cs-cloud.env)")
	}
	client := workflowrunner.NewClient(cfg.Workflow.BackendBaseURL, "", func() (*provider.Credentials, error) {
		return creds, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	projects, err := client.GetProjects(ctx, wsID)
	if err != nil {
		return err
	}
	for _, p := range projects {
		fmt.Printf("%s  %s\n", p.ID, p.Name)
	}
	return nil
}

// loadTaskEnvFile loads KEY=VALUE lines from .cs-cloud.env in the current
// directory (the task workdir) into the process env. cs-cloud writes this file
// at task start with the CS_CLOUD_* task variables; loading it here lets the
// workflow CLIs resolve task context from a file, which is more robust than
// relying on env propagation through the agent subprocess. A missing file is a
// no-op (CLIs fall back to the process env / flags).
func loadTaskEnvFile() {
	cwd, err := os.Getwd()
	if err != nil {
		return
	}
	loadTaskEnvFileFrom(cwd)
}

// findTaskEnvFile searches dir then each parent directory for the task env
// file, returning the first match. Returns "" at the filesystem root so the
// caller can treat a missing file as a no-op.
func findTaskEnvFile(dir string) string {
	for {
		p := filepath.Join(dir, workflowrunner.TaskEnvFileName)
		if info, err := os.Lstat(p); err == nil && info.Mode().IsRegular() {
			return p
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// popTaskFlag scans args for --task <id> (or --task=<id>, anywhere in the args)
// and removes it, returning the remaining args plus the id ("" when absent).
// Only the first occurrence is consumed. The agent passes the task id it
// learned from the task prompt; the CLI uses it to locate .cs-cloud.env without
// relying on the process cwd.
func popTaskFlag(args []string) (remaining []string, taskID string) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--task" && i+1 < len(args):
			return spliceOut(args, i, 2), args[i+1]
		case strings.HasPrefix(args[i], "--task="):
			return spliceOut(args, i, 1), strings.TrimPrefix(args[i], "--task=")
		}
	}
	return args, ""
}

// taskIDFromEnvFile reads CS_CLOUD_TASK_ID from a .cs-cloud.env file. Returns ""
// when the file is unreadable, too large, or does not carry the key.
func taskIDFromEnvFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTaskEnvFileBytes+1))
	if err != nil || len(b) > maxTaskEnvFileBytes {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == workflowrunner.EnvTaskID {
			return stripEnvQuotes(strings.TrimSpace(v))
		}
	}
	return ""
}

// findTaskRootByTaskID scans <workspacesRoot>/*/tasks/*/.cs-cloud.env for the
// one whose CS_CLOUD_TASK_ID equals taskID, and returns its directory. This is
// the rework/resume-safe locator: the runner may reuse a prior round's
// directory (named after a previous task id) but always writes the CURRENT task
// id into .cs-cloud.env, so matching by content — not by directory name — finds
// the real task root. Returns "" when there is no match. If a stale duplicate
// ever matches (should not, task ids are unique), the most recently modified
// file wins. Read-only: no new files, no GC coupling.
func findTaskRootByTaskID(workspacesRoot, taskID string) string {
	if workspacesRoot == "" || taskID == "" {
		return ""
	}
	// O(1): pointer the runner wrote at task start (<runs>/<taskID> → task
	// root). Confirm .cs-cloud.env is actually present so a stale pointer (workdir
	// GC'd but pointer leaked from a crash) falls through to the scan.
	if ptr := workflowrunner.ReadTaskPointer(workspacesRoot, taskID); ptr != "" {
		if info, err := os.Lstat(filepath.Join(ptr, workflowrunner.TaskEnvFileName)); err == nil && info.Mode().IsRegular() {
			return ptr
		}
	}
	// Fallback: scan every task dir's .cs-cloud.env for the matching task id.
	pattern := filepath.Join(workspacesRoot, "*", "tasks", "*", workflowrunner.TaskEnvFileName)
	files, err := filepath.Glob(pattern)
	if err != nil {
		return ""
	}
	var bestPath string
	var bestMTime time.Time
	for _, f := range files {
		if taskIDFromEnvFile(f) != taskID {
			continue
		}
		// On a (should-not-happen) duplicate, keep the most recently modified.
		if bestPath == "" || fileMTime(f).After(bestMTime) {
			bestPath, bestMTime = f, fileMTime(f)
		}
	}
	if bestPath == "" {
		return ""
	}
	return filepath.Dir(bestPath)
}

// fileMTime returns the file's modification time, or the zero time on error.
func fileMTime(path string) time.Time {
	if fi, err := os.Stat(path); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// spliceOut returns args with n elements at index i removed.
func spliceOut(args []string, i, n int) []string {
	out := make([]string, 0, len(args)-n)
	out = append(out, args[:i]...)
	out = append(out, args[i+n:]...)
	return out
}

// loadTaskEnvFileFrom loads KEY=VALUE lines from .cs-cloud.env located by walking
// up from startDir. startDir is either the process cwd (loadTaskEnvFile) or the
// task root that findTaskRootByTaskID resolved from the agent's `--task <id>`
// (workflowCmd). The file lives in the task root, but the agent often runs
// in-task CLIs from a cloned repo subdir, so walking up resolves it regardless
// of where in the task tree the agent is; a missing file is a no-op (CLIs fall
// back to process env).
func loadTaskEnvFileFrom(startDir string) {
	path := findTaskEnvFile(startDir)
	if path == "" {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxTaskEnvFileBytes+1))
	if err != nil || len(b) > maxTaskEnvFileBytes {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		os.Setenv(strings.TrimSpace(k), stripEnvQuotes(strings.TrimSpace(v)))
	}
}

// stripEnvQuotes removes a single layer of surrounding double or single quotes
// from a .env value (so `KEY="value"` and `KEY=value` parse the same).
func stripEnvQuotes(v string) string {
	if len(v) >= 2 {
		first, last := v[0], v[len(v)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
