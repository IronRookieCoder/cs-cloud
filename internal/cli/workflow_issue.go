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

// workflowIssueCmd implements `cs-cloud workflow issue <subcommand>`.
// Supports: get, list, comment add/list — the operations an in-task agent or
// developer needs (read context, browse issues, communicate).
func workflowIssueCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud workflow issue <get|list|create|status|comment> ...")
	}
	switch args[0] {
	case "get":
		return workflowIssueGet(a, args[1:])
	case "list":
		return workflowIssueList(a, args[1:])
	case "create":
		return workflowIssueCreate(a, args[1:])
	case "status":
		return workflowIssueStatus(a, args[1:])
	case "comment":
		return workflowIssueCommentCmd(a, args[1:])
	default:
		return fmt.Errorf("unknown workflow issue command: %s", args[0])
	}
}

// workflowIssueList: `cs-cloud workflow issue list`
func workflowIssueList(a *app.App, _ []string) error {
	client, wsID, ctx, cancel, err := issueClient(a)
	if err != nil {
		return err
	}
	defer cancel()
	issues, err := client.ListIssues(ctx, wsID)
	if err != nil {
		return err
	}
	for _, iss := range issues {
		fmt.Printf("%s  [%s]  %s\n", iss.ID, iss.Status, iss.Title)
	}
	return nil
}

// workflowIssueCreate: `cs-cloud workflow issue create --title "..." [--description "..."]`
func workflowIssueCreate(a *app.App, args []string) error {
	var title, description string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title":
			if i+1 >= len(args) {
				return fmt.Errorf("--title needs a value")
			}
			title = args[i+1]
			i++
		case "--description":
			if i+1 >= len(args) {
				return fmt.Errorf("--description needs a value")
			}
			description = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	if title == "" {
		return fmt.Errorf("--title is required")
	}
	client, wsID, ctx, cancel, err := issueClient(a)
	if err != nil {
		return err
	}
	defer cancel()
	issue, err := client.CreateIssue(ctx, wsID, title, description)
	if err != nil {
		return err
	}
	fmt.Printf("created %s  [%s]  %s\n", issue.ID, issue.Status, issue.Title)
	return nil
}

// workflowIssueStatus: `cs-cloud workflow issue status <issue-id> <status>`
func workflowIssueStatus(a *app.App, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: cs-cloud workflow issue status <issue-id> <status>")
	}
	issueID, status := args[0], args[1]
	client, wsID, ctx, cancel, err := issueClient(a)
	if err != nil {
		return err
	}
	defer cancel()
	if err := client.UpdateIssueStatus(ctx, wsID, issueID, status); err != nil {
		return err
	}
	fmt.Printf("updated %s → %s\n", issueID, status)
	return nil
}

// workflowIssueGet: `cs-cloud workflow issue get <issue-id>`
func workflowIssueGet(a *app.App, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud workflow issue get <issue-id>")
	}
	client, wsID, ctx, cancel, err := issueClient(a)
	if err != nil {
		return err
	}
	defer cancel()
	issue, err := client.GetIssue(ctx, wsID, args[0])
	if err != nil {
		return err
	}
	fmt.Printf("ID:     %s\n", issue.ID)
	fmt.Printf("Title:  %s\n", issue.Title)
	fmt.Printf("Status: %s\n", issue.Status)
	return nil
}

func workflowIssueCommentCmd(a *app.App, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud workflow issue comment <add|list> ...")
	}
	switch args[0] {
	case "add":
		return workflowIssueCommentAdd(a, args[1:])
	case "list":
		return workflowIssueCommentList(a, args[1:])
	default:
		return fmt.Errorf("unknown comment command: %s", args[0])
	}
}

// workflowIssueCommentAdd: `cs-cloud workflow issue comment add <issue-id> --content "..."`
func workflowIssueCommentAdd(a *app.App, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud workflow issue comment add <issue-id> --content \"...\"")
	}
	issueID := args[0]
	var content string
	for i := 1; i < len(args); i++ {
		if args[i] == "--content" && i+1 < len(args) {
			content = args[i+1]
			i++
		}
	}
	if content == "" {
		return fmt.Errorf("--content is required")
	}
	client, wsID, ctx, cancel, err := issueClient(a)
	if err != nil {
		return err
	}
	defer cancel()
	if err := client.CreateIssueComment(ctx, wsID, issueID, content); err != nil {
		return err
	}
	fmt.Println("comment added")
	return nil
}

// workflowIssueCommentList: `cs-cloud workflow issue comment list <issue-id>`
func workflowIssueCommentList(a *app.App, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud workflow issue comment list <issue-id>")
	}
	client, wsID, ctx, cancel, err := issueClient(a)
	if err != nil {
		return err
	}
	defer cancel()
	comments, err := client.ListIssueComments(ctx, wsID, args[0])
	if err != nil {
		return err
	}
	for _, c := range comments {
		ts := c.CreatedAt.Format("2006-01-02 15:04")
		author := c.AuthorName
		if author == "" {
			author = c.AuthorType
		}
		fmt.Printf("%s  %s: %s\n", ts, author, c.Content)
	}
	return nil
}

// issueClient builds a multica client + resolves the workspace ID from the task
// env (MULTICA_WORKSPACE_ID). Shared by all workflow issue subcommands.
func issueClient(a *app.App) (*workflowrunner.Client, string, context.Context, context.CancelFunc, error) {
	cfg := a.Config()
	creds, err := a.Credentials()
	if err != nil {
		return nil, "", nil, nil, err
	}
	wsID := os.Getenv("MULTICA_WORKSPACE_ID")
	if wsID == "" {
		return nil, "", nil, nil, fmt.Errorf("MULTICA_WORKSPACE_ID not set (run inside a task or export it)")
	}
	client := workflowrunner.NewClient(cfg.Workflow.MulticaBaseURL, "", func() (*provider.Credentials, error) {
		return creds, nil
	})
	timeout := cfg.Workflow.AgentTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	return client, wsID, ctx, cancel, nil
}
