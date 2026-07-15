package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"cs-cloud/internal/workflow"
)

// TaskRunner executes a single workflow task by preparing a worktree,
// invoking the configured agent CLI, and reporting status back to multica.
type TaskRunner struct {
	workspaceManager *WorkspaceManager
	client           *Client
	agentTimeout     time.Duration
	allowedAgents    []string
}

// NewTaskRunner creates a new TaskRunner.
func NewTaskRunner(wm *WorkspaceManager, client *Client, timeout time.Duration, allowedAgents []string) *TaskRunner {
	return &TaskRunner{
		workspaceManager: wm,
		client:           client,
		agentTimeout:     timeout,
		allowedAgents:    allowedAgents,
	}
}

// Run prepares the worktree, marks the task started, runs the agent, and
// reports the final status to multica.
func (tr *TaskRunner) Run(ctx context.Context, payload workflow.TaskRunPayload) error {
	repoURL, err := tr.resolveRepoURL(ctx, payload.WorkspaceID, payload.ProjectID)
	if err != nil {
		return tr.fail(ctx, payload.TaskID, fmt.Errorf("resolve repo: %w", err))
	}

	worktree, err := tr.workspaceManager.CreateWorktree(payload.WorkspaceID, payload.TaskID, repoURL, "HEAD")
	if err != nil {
		return tr.fail(ctx, payload.TaskID, fmt.Errorf("prepare worktree: %w", err))
	}

	if err := tr.client.StartTask(ctx, payload.TaskID); err != nil {
		return err
	}

	if err := tr.validateAgent(payload.Agent); err != nil {
		return tr.fail(ctx, payload.TaskID, err)
	}

	cmd := exec.CommandContext(ctx, payload.Agent)
	cmd.Dir = worktree
	cmd.Env = tr.buildEnv(payload, worktree)
	cmd.Stdin = strings.NewReader(payload.Prompt)

	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = tr.client.PostTaskMessages(ctx, payload.TaskID, string(out))
		return tr.fail(ctx, payload.TaskID, fmt.Errorf("agent exit: %w", err))
	}

	_ = tr.client.PostTaskMessages(ctx, payload.TaskID, string(out))
	return tr.client.CompleteTask(ctx, payload.TaskID, map[string]any{"status": "ok"})
}

func (tr *TaskRunner) resolveRepoURL(ctx context.Context, workspaceID, projectID string) (string, error) {
	if projectID == "" || workspaceID == "" {
		return "", nil
	}
	projects, err := tr.client.GetProjects(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.ID == projectID {
			return p.RepoURL, nil
		}
	}
	return "", nil
}

func (tr *TaskRunner) validateAgent(agent string) error {
	if agent == "" {
		return fmt.Errorf("no agent specified")
	}
	for _, a := range tr.allowedAgents {
		if a == agent {
			return nil
		}
	}
	return fmt.Errorf("agent %q is not in the allowed list", agent)
}

func (tr *TaskRunner) buildEnv(payload workflow.TaskRunPayload, worktree string) []string {
	env := []string{
		"MULTICA_WORKSPACE_ID=" + payload.WorkspaceID,
		"MULTICA_TASK_ID=" + payload.TaskID,
		"MULTICA_PROMPT=" + payload.Prompt,
		"CS_CLOUD_WORKTREE=" + worktree,
	}
	for k, v := range payload.Env {
		env = append(env, k+"="+v)
	}
	return env
}

func (tr *TaskRunner) fail(ctx context.Context, taskID string, err error) error {
	_ = tr.client.FailTask(ctx, taskID, err.Error())
	return err
}
