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
		return fmt.Errorf("unknown workspace action: %s", args[0])
	}
}

func workflowWorkspaceList(a *app.App) error {
	cfg := a.Config()
	cache := workflow.NewCache(cfg.Workflow.CacheDir)
	wss, err := cache.ReadWorkspaces()
	if err != nil {
		return err
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
		return err
	}
	client := workflowrunner.NewClient(cfg.Workflow.BackendBaseURL, "", func() (*provider.Credentials, error) {
		return creds, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), cfg.Workflow.AgentTimeout)
	defer cancel()
	wss, err := client.GetWorkspaces(ctx)
	if err != nil {
		return err
	}
	cache := workflow.NewCache(cfg.Workflow.CacheDir)
	if err := cache.WriteWorkspaces(wss); err != nil {
		return err
	}
	fmt.Printf("Synced %d workspaces\n", len(wss))
	return nil
}
