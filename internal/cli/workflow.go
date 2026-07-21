package cli

import (
	"fmt"

	"cs-cloud/internal/app"
)

func workflowCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		printWorkflowUsage()
		return nil
	}

	switch args[0] {
	case "workspace":
		return workflowWorkspaceCmd(a, args[1:])
	case "issue":
		return workflowIssueCmd(a, args[1:])
	case "project":
		return workflowProjectCmd(a, args[1:])
	case "task":
		return workflowTaskCmd(a, args[1:])
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
		{"issue", "List/create/update issues"},
		{"project", "List projects"},
		{"task", "Run/check workflow tasks"},
	}
	fmt.Print(renderKV(cmds))
}

func workflowProjectCmd(a *app.App, args []string) error {
	_ = a
	_ = args
	return fmt.Errorf("workflow project commands are not implemented yet")
}

func workflowTaskCmd(a *app.App, args []string) error {
	_ = a
	_ = args
	return fmt.Errorf("workflow task commands are not implemented yet")
}
