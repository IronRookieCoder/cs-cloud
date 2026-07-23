package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflowrunner"
)

func workflowCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		printWorkflowUsage()
		return nil
	}

	switch args[0] {
	case "workspace":
		return workflowWorkspaceCmd(a, args[1:])
	case "project":
		return workflowProjectCmd(a, args[1:])
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
	wsID := os.Getenv("MULTICA_WORKSPACE_ID")
	if wsID == "" {
		return fmt.Errorf("MULTICA_WORKSPACE_ID not set")
	}
	client := workflowrunner.NewClient(cfg.Workflow.MulticaBaseURL, "", func() (*provider.Credentials, error) {
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
