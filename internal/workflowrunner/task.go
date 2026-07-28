package workflowrunner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

const (
	// AgentCsc is the built-in "cs-cloud" agent name.
	AgentCsc = "csc"

	// CLI flags used when invoking the csc agent.
	CscOutputFormatFlag = "--output-format"
	CscOutputFormatText = "text"

	// Environment variables injected into every agent process.
	EnvWorkspaceID     = "MULTICA_WORKSPACE_ID"
	EnvTaskID          = "MULTICA_TASK_ID"
	EnvPrompt          = "MULTICA_PROMPT"
	EnvCSCloudWorktree = "CS_CLOUD_WORKTREE"
	// For in-task CLIs (cs-cloud gitea submit) that call the server.
	EnvServerURL = "MULTICA_SERVER_URL"
	EnvToken     = "MULTICA_TOKEN"
	// EnvCSCloudServerURL lets in-task `cs-cloud repo checkout` reach the
	// daemon's localserver RPC. Threaded in by the daemon via SetLocalServerURL.
	EnvCSCloudServerURL = "CS_CLOUD_SERVER_URL"
)

// TaskRunner executes a single workflow task by preparing a worktree and
// invoking the configured agent CLI. Status reporting is handled by the Driver.
type TaskRunner struct {
	workspaceManager *WorkspaceManager
	agentTimeout     time.Duration
	allowedAgents    []string
	sessionRunner    SessionRunner
	// serverBaseURL + tokenProvider let buildEnv inject MULTICA_SERVER_URL +
	// MULTICA_TOKEN so task-invoked CLIs (e.g. `cs-cloud gitea submit`)
	// can call the server's daemon-auth API. Set via SetServerEndpoint.
	serverBaseURL string
	tokenProvider func() (*provider.Credentials, error)
	// localServerURL is the daemon's localserver listen URL, injected into
	// task env as CS_CLOUD_SERVER_URL so in-task `cs-cloud repo checkout`
	// can reach the localserver RPC. Set via SetLocalServerURL.
	localServerURL string
}

// NewTaskRunner creates a new TaskRunner.
func NewTaskRunner(wm *WorkspaceManager, timeout time.Duration, allowedAgents []string) *TaskRunner {
	return &TaskRunner{
		workspaceManager: wm,
		agentTimeout:     timeout,
		allowedAgents:    allowedAgents,
	}
}

// SetServerEndpoint injects the server base URL + token provider so the task
// env can carry MULTICA_SERVER_URL + MULTICA_TOKEN for in-task CLIs.
func (tr *TaskRunner) SetServerEndpoint(baseURL string, tp func() (*provider.Credentials, error)) {
	tr.serverBaseURL = baseURL
	tr.tokenProvider = tp
}

// SetLocalServerURL lets the daemon thread its localserver listen URL into the
// task env so in-task `cs-cloud repo checkout` can reach the RPC.
func (tr *TaskRunner) SetLocalServerURL(url string) {
	tr.localServerURL = url
}

// SetSessionRunner injects a runner that executes prompts inside an already
// bound local csc session. When set and the task agent is csc, the task runs
// in the bound session instead of a one-shot CLI process.
func (tr *TaskRunner) SetSessionRunner(r SessionRunner) {
	tr.sessionRunner = r
}

// withAgentTimeout derives a child context bounded by the configured
// agentTimeout when it is positive, so a hung CLI or CSC session cannot block
// a task indefinitely. A zero agentTimeout leaves the caller context as-is.
func (tr *TaskRunner) withAgentTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if tr.agentTimeout > 0 {
		return context.WithTimeout(ctx, tr.agentTimeout)
	}
	return ctx, func() {}
}

// RunCSCSession runs the task prompt in the bound local csc session. It falls
// back to the one-shot CLI if no session runner is configured.
func (tr *TaskRunner) RunCSCSession(ctx context.Context, payload workflow.TaskRunPayload, worktree, sessionID string) ([]byte, error) {
	if tr.sessionRunner != nil {
		ctx, cancel := tr.withAgentTimeout(ctx)
		defer cancel()
		return tr.sessionRunner.RunSession(ctx, sessionID, worktree, payload.Prompt, tr.buildEnv(payload, worktree))
	}

	agentPath, err := exec.LookPath(payload.Agent)
	if err != nil {
		return nil, fmt.Errorf("resolve agent %q: %w", payload.Agent, err)
	}
	return tr.RunPrepared(ctx, payload, worktree, agentPath)
}

// Run prepares the worktree and runs the agent. It returns the combined
// stdout/stderr and any execution error. The caller is responsible for
// reporting task status to the server.
func (tr *TaskRunner) Run(ctx context.Context, payload workflow.TaskRunPayload) ([]byte, error) {
	worktree, agentPath, err := tr.Prepare(ctx, payload)
	if err != nil {
		return nil, err
	}
	return tr.RunPrepared(ctx, payload, worktree, agentPath)
}

// Prepare determines the task root (reusing the prior workdir when resuming,
// else a fresh per-task dir), ensures it exists, and pre-warms the mirror
// caches for the task's Repos[] in the background so the agent's on-demand
// `cs-cloud repo checkout` is fast. It returns the task root (the agent's cwd);
// per-repo worktrees are created lazily by checkout, not here.
func (tr *TaskRunner) Prepare(ctx context.Context, payload workflow.TaskRunPayload) (worktree string, agentPath string, err error) {
	if err := tr.validateAgent(payload.Agent); err != nil {
		return "", "", err
	}

	// Resolve the agent executable using the parent process PATH before the
	// task environment (which may override PATH) is applied. This prevents an
	// allowed agent name from being redirected to an attacker-controlled binary.
	agentPath, err = exec.LookPath(payload.Agent)
	if err != nil {
		return "", "", fmt.Errorf("resolve agent %q: %w", payload.Agent, err)
	}

	taskRoot := payload.PriorWorkDir
	if taskRoot == "" || !dirExists(taskRoot) {
		taskRoot = tr.workspaceManager.TaskWorktreeDir(payload.WorkspaceID, payload.TaskID)
	}
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		return "", "", fmt.Errorf("prepare task root: %w", err)
	}

	// Pre-warm mirror caches for all advertised repos (best-effort, background).
	// The agent's checkout re-ensures (serialized per cache), so a missed warm-up
	// just means a cold clone at checkout time. Token is per-role: a delivery
	// repo (Gitea) must clone with MULTICA_REPO_TOKEN, not the GitLab PAT, or the
	// pre-warm 401s silently — sharing tokenForRepo with Driver.CheckoutRepo
	// keeps the two paths consistent.
	go func() {
		for _, r := range payload.Repos {
			if r.URL == "" {
				continue
			}
			role := lookupRepoRole(payload.Repos, r.URL)
			_, _ = tr.workspaceManager.EnsureRepoReady(payload.WorkspaceID, r.URL, tokenForRepo(role, payload.Env))
		}
	}()

	return taskRoot, agentPath, nil
}

// dirExists reports whether path is an existing directory.
func dirExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

// RunPrepared runs the agent in the already-prepared worktree. It returns the
// combined stdout/stderr and any execution error.
func (tr *TaskRunner) RunPrepared(ctx context.Context, payload workflow.TaskRunPayload, worktree, agentPath string) ([]byte, error) {
	ctx, cancel := tr.withAgentTimeout(ctx)
	defer cancel()
	args := tr.buildArgs(payload)

	cmd := exec.CommandContext(ctx, agentPath, args...)
	cmd.Dir = worktree
	cmd.Env = tr.buildEnv(payload, worktree)

	return cmd.CombinedOutput()
}

func (tr *TaskRunner) buildArgs(payload workflow.TaskRunPayload) []string {
	if payload.Agent == AgentCsc {
		// Run csc in non-interactive print mode so workflow tasks complete
		// instead of starting the interactive TUI and never exiting.
		return []string{"-p", "--permission-mode", "bypassPermissions", CscOutputFormatFlag, CscOutputFormatText, payload.Prompt}
	}
	switch filepath.Base(payload.Agent) {
	case "sh", "bash", "zsh":
		return []string{"-c", payload.Prompt}
	}
	return []string{payload.Prompt}
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
	env = setEnv(env, EnvWorkspaceID, payload.WorkspaceID)
	env = setEnv(env, EnvTaskID, payload.TaskID)
	env = setEnv(env, EnvPrompt, payload.Prompt)
	env = setEnv(env, EnvCSCloudWorktree, worktree)
	// CS_CLOUD_SERVER_URL lets in-task `cs-cloud repo checkout` reach the
	// daemon's localserver RPC. Threaded in by the daemon after it binds.
	if tr.localServerURL != "" {
		env = setEnv(env, EnvCSCloudServerURL, tr.localServerURL)
	}
	// MULTICA_SERVER_URL + MULTICA_TOKEN so in-task CLIs (cs-cloud gitea
	// submit) can authenticate to the server's daemon API. These are the
	// daemon's own endpoint + credentials — cs-cloud owns this auth, not the server.
	if tr.serverBaseURL != "" {
		env = setEnv(env, EnvServerURL, tr.serverBaseURL)
	}
	if tr.tokenProvider != nil {
		if creds, err := tr.tokenProvider(); err == nil && creds != nil && creds.AccessToken != "" {
			env = setEnv(env, EnvToken, creds.AccessToken)
		}
	}
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
