package workflow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"cs-cloud/internal/workflow"
)

const (
	// AgentCsc is the built-in "cs-cloud" agent name.
	AgentCsc = "csc"

	// CLI flags used when invoking the csc agent.
	CscOutputFormatFlag = "--output-format"
	CscOutputFormatText = "text"

	// Environment variables injected into every agent process.
	EnvMulticaWorkspaceID = "MULTICA_WORKSPACE_ID"
	EnvMulticaTaskID      = "MULTICA_TASK_ID"
	EnvMulticaPrompt      = "MULTICA_PROMPT"
	EnvCSCloudWorktree    = "CS_CLOUD_WORKTREE"
)

// TaskRunner executes a single workflow task by preparing a worktree and
// invoking the configured agent CLI. Status reporting is handled by the Driver.
type TaskRunner struct {
	workspaceManager *WorkspaceManager
	agentTimeout     time.Duration
	allowedAgents    []string
}

// NewTaskRunner creates a new TaskRunner.
func NewTaskRunner(wm *WorkspaceManager, timeout time.Duration, allowedAgents []string) *TaskRunner {
	return &TaskRunner{
		workspaceManager: wm,
		agentTimeout:     timeout,
		allowedAgents:    allowedAgents,
	}
}

// Run prepares the worktree and runs the agent. It returns the combined
// stdout/stderr and any execution error. The caller is responsible for
// reporting task status to multica.
func (tr *TaskRunner) Run(ctx context.Context, payload workflow.TaskRunPayload) ([]byte, error) {
	repoURL, err := tr.resolveRepoURL(ctx, payload.WorkspaceID, payload.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("resolve repo: %w", err)
	}

	if err := tr.validateAgent(payload.Agent); err != nil {
		return nil, err
	}

	// Resolve the agent executable using the parent process PATH before the
	// task environment (which may override PATH) is applied. This prevents an
	// allowed agent name from being redirected to an attacker-controlled binary.
	agentPath, err := exec.LookPath(payload.Agent)
	if err != nil {
		return nil, fmt.Errorf("resolve agent %q: %w", payload.Agent, err)
	}

	worktree, err := tr.workspaceManager.CreateWorktree(payload.WorkspaceID, payload.TaskID, repoURL, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("prepare worktree: %w", err)
	}

	args := []string{payload.Prompt}
	if payload.Agent == AgentCsc {
		args = append(args, CscOutputFormatFlag, CscOutputFormatText)
	}

	cmd := exec.CommandContext(ctx, agentPath, args...)
	cmd.Dir = worktree
	cmd.Env = tr.buildEnv(payload, worktree)

	return cmd.CombinedOutput()
}

func (tr *TaskRunner) resolveRepoURL(ctx context.Context, workspaceID, projectID string) (string, error) {
	_ = ctx
	if projectID == "" || workspaceID == "" {
		return "", nil
	}
	// Project-to-repo resolution is currently a no-op. In a full implementation
	// this would query the multica backend for the project's repo_url.
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
	env := os.Environ()
	for k, v := range payload.Env {
		env = setEnv(env, k, v)
	}
	env = setEnv(env, EnvMulticaWorkspaceID, payload.WorkspaceID)
	env = setEnv(env, EnvMulticaTaskID, payload.TaskID)
	env = setEnv(env, EnvMulticaPrompt, payload.Prompt)
	env = setEnv(env, EnvCSCloudWorktree, worktree)
	return env
}

func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}
