package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflowrunner"
)

func workflowCmd(a *app.App, args []string) error {
	// Resolve task context from .cs-cloud.env in the cwd (the task workdir).
	// cs-cloud writes this file at task start; loading it here lets the workflow
	// CLIs work even when env propagation through the agent subprocess fails.
	loadTaskEnvFile()
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
	b, err := os.ReadFile(filepath.Join(cwd, workflowrunner.TaskEnvFileName))
	if err != nil {
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
