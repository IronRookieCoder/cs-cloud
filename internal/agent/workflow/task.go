package workflow

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"cs-cloud/internal/workflow"
)

// TaskRunner executes a single workflow task by preparing a worktree,
// notifying multica that the task has started, running the agent CLI,
// posting output messages, and reporting completion or failure.
type TaskRunner struct {
	workspaceManager *WorkspaceManager
	client           *Client
	agentTimeout     time.Duration
}

// NewTaskRunner creates a new TaskRunner.
func NewTaskRunner(wm *WorkspaceManager, client *Client, timeout time.Duration) *TaskRunner {
	return &TaskRunner{
		workspaceManager: wm,
		client:           client,
		agentTimeout:     timeout,
	}
}

// Run prepares the worktree, starts the task, executes the agent, and reports
// the final status to multica.
func (tr *TaskRunner) Run(ctx context.Context, payload workflow.TaskRunPayload) error {
	worktree, err := tr.workspaceManager.CreateWorktree(payload.WorkspaceID, payload.TaskID, "", "HEAD")
	if err != nil {
		return tr.fail(payload.TaskID, fmt.Errorf("prepare worktree: %w", err))
	}

	if err := tr.client.StartTask(payload.TaskID); err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, payload.Agent)
	cmd.Dir = worktree
	cmd.Env = tr.buildEnv(payload, worktree)

	out, err := cmd.CombinedOutput()
	if err != nil {
		_ = tr.client.PostTaskMessages(payload.TaskID, string(out))
		return tr.fail(payload.TaskID, fmt.Errorf("agent exit: %w", err))
	}

	_ = tr.client.PostTaskMessages(payload.TaskID, string(out))
	return tr.client.CompleteTask(payload.TaskID, map[string]any{"status": "ok"})
}

// buildEnv constructs the environment variables passed to the agent process.
func (tr *TaskRunner) buildEnv(payload workflow.TaskRunPayload, worktree string) []string {
	env := []string{
		"MULTICA_WORKSPACE_ID=" + payload.WorkspaceID,
		"MULTICA_TASK_ID=" + payload.TaskID,
		"CS_CLOUD_WORKTREE=" + worktree,
	}
	for k, v := range payload.Env {
		env = append(env, k+"="+v)
	}
	return env
}

// fail reports the task as failed and returns the original error.
func (tr *TaskRunner) fail(taskID string, err error) error {
	_ = tr.client.FailTask(taskID, err.Error())
	return err
}
