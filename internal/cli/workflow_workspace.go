package cli

import (
	"context"
	"fmt"

	"cs-cloud/internal/app"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner"
)

func workflowWorkspaceCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		return workflowWorkspaceList(a)
	}
	switch args[0] {
	case "list":
		return workflowWorkspaceList(a)
	case "sync":
		return workflowWorkspaceSync(a)
	default:
		// Always exit 0 with corrective text — a non-zero exit would derail the
		// agent loop without helping it recover.
		fmt.Println("Unknown workflow workspace action: " + args[0] + ". Valid actions: list, sync.")
		return nil
	}
}

func workflowWorkspaceList(a *app.App) error {
	cfg := a.Config()
	cache := workflow.NewCache(cfg.Workflow.CacheDir)
	wss, err := cache.ReadWorkspaces()
	if err != nil {
		fmt.Println("Could not read the workspace cache: " + err.Error() + ". Run `cs-cloud workflow workspace sync` to populate it.")
		return nil
	}
	if len(wss) == 0 {
		fmt.Println("No workspaces cached. Run `cs-cloud workflow workspace sync` to populate the list.")
		return nil
	}
	for _, ws := range wss {
		fmt.Printf("%s  %s\n", ws.ID, ws.Name)
	}
	return nil
}

func workflowWorkspaceSync(a *app.App) error {
	cfg := a.Config()
	creds, err := a.Credentials()
	if err != nil {
		fmt.Println("Could not sync workspaces (no credentials): " + err.Error() + ". Run `cs-cloud login` first.")
		return nil
	}
	client := workflowrunner.NewClient(cfg.Workflow.BackendBaseURL, "", func() (*provider.Credentials, error) {
		return creds, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Workflow.AgentTimeout)
	defer cancel()
	wss, err := client.GetWorkspaces(ctx)
	if err != nil {
		fmt.Println("Could not sync workspaces from the server: " + err.Error() + ". Check connectivity and CS_CLOUD_WORKFLOW_BACKEND_BASE_URL.")
		return nil
	}
	cache := workflow.NewCache(cfg.Workflow.CacheDir)
	if err := cache.WriteWorkspaces(wss); err != nil {
		fmt.Println("Synced from the server but could not write the cache: " + err.Error() + ".")
		return nil
	}
	fmt.Printf("Synced %d workspaces\n", len(wss))
	return nil
}
