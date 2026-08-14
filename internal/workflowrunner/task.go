package workflowrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
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
	EnvWorkspaceID     = "CS_CLOUD_WORKSPACE_ID"
	EnvTaskID          = "CS_CLOUD_TASK_ID"
	EnvAgentID         = "CS_CLOUD_AGENT_ID"
	EnvPrompt          = "CS_CLOUD_PROMPT"
	EnvCSCloudWorktree = "CS_CLOUD_WORKTREE"
	// For in-task CLIs (cs-cloud gitea submit) that call the server.
	EnvServerURL = "CS_CLOUD_BACKEND_URL"
	EnvToken     = "CS_CLOUD_TOKEN"
	// EnvLocalServerURL is this device's localserver base URL, letting in-task
	// CLIs (e.g. `cs-cloud workflow task complete`) call back into the driver
	// without going through the server.
	EnvLocalServerURL = "CS_CLOUD_LOCAL_URL"
	// EnvLocalServerAPIKey is the localserver API key (when one is configured),
	// attached to in-task completion callbacks so they pass the localserver's
	// apiAuth middleware instead of 401-ing.
	EnvLocalServerAPIKey = "CS_CLOUD_LOCAL_API_KEY"
	// deliveryRepoPrepareTimeout bounds delivery repo clone/fetch so a stalled
	// Git operation cannot block session startup indefinitely when the caller
	// context has no deadline. Generous because delivery repos can be large and
	// preparation is best-effort: a timeout only logs a warning, and the agent
	// can still clone manually from .cs-cloud.repos.
	deliveryRepoPrepareTimeout = 2 * time.Minute
	// TaskEnvFileName is the file cs-cloud writes into each task workdir
	// containing the CS_CLOUD_* task variables. In-task CLIs load it (see
	// cli.loadTaskEnvFile) so they resolve task context from a file rather than
	// relying on env propagation through the agent subprocess.
	TaskEnvFileName = ".cs-cloud.env"
	// TaskReposFileName is the human-readable repository map for the task.
	// Tokens stay in .cs-cloud.env; this file only describes repository purpose,
	// branches, and deliverable paths for the agent to inspect.
	TaskReposFileName = ".cs-cloud.repos"
)

// TaskRunner executes a single workflow task by preparing a worktree and
// invoking the configured agent CLI. Status reporting is handled by the Driver.
type TaskRunner struct {
	workspaceManager *WorkspaceManager
	agentTimeout     time.Duration
	allowedAgents    []string
	sessionRunner    SessionRunner
	agentEnv         map[string]string
	// serverBaseURL + tokenProvider let buildEnv inject CS_CLOUD_BACKEND_URL +
	// CS_CLOUD_TOKEN so task-invoked CLIs (e.g. `cs-cloud gitea submit`)
	// can call the server's daemon-auth API. Set via SetServerEndpoint.
	serverBaseURL string
	tokenProvider func() (*provider.Credentials, error)
	// localServerURL is this device's localserver URL, injected as
	// CS_CLOUD_LOCAL_URL so in-task CLIs can call back into the driver (e.g.
	// the "complete task" tool). Set via SetLocalServerURL.
	localServerURL string
	// localServerAPIKey is the localserver API key (empty when none configured),
	// injected as CS_CLOUD_LOCAL_API_KEY so in-task completion callbacks can
	// authenticate to the localserver's apiAuth middleware. Set via SetLocalAPIKey.
	localServerAPIKey string
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
// env can carry CS_CLOUD_BACKEND_URL + CS_CLOUD_TOKEN for in-task CLIs.
func (tr *TaskRunner) SetServerEndpoint(baseURL string, tp func() (*provider.Credentials, error)) {
	tr.serverBaseURL = baseURL
	tr.tokenProvider = tp
}

// SetAgentEnv injects the daemon-level agent environment. It is applied before
// task payload env so per-task values can still override it.
func (tr *TaskRunner) SetAgentEnv(env map[string]string) {
	tr.agentEnv = env
}

// SetLocalServerURL injects this device's localserver URL so buildEnv can carry
// CS_CLOUD_LOCAL_URL for in-task CLIs that call back into the driver.
func (tr *TaskRunner) SetLocalServerURL(url string) {
	tr.localServerURL = url
}

// SetLocalAPIKey injects the localserver API key so buildEnv can carry
// CS_CLOUD_LOCAL_API_KEY for in-task completion callbacks. Empty when the
// localserver has no API key configured (auth middleware is then a no-op).
func (tr *TaskRunner) SetLocalAPIKey(key string) {
	tr.localServerAPIKey = key
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
		env := tr.buildEnv(payload, worktree)
		if err := writeTaskEnvFile(worktree, env); err != nil {
			return nil, fmt.Errorf("write task env: %w", err)
		}
		// Record a pointer (<runs>/<taskID> → worktree) so the in-task CLI can
		// locate .cs-cloud.env in O(1) by task id. Best-effort: the CLI's scan
		// fallback covers a missing/stale pointer. Removed when the session
		// returns (defer); orphan pointers from a crash are tiny and age out
		// with the workdir via GC.
		if tr.workspaceManager != nil {
			_ = WriteTaskPointer(tr.workspaceManager.Root(), payload.TaskID, worktree)
			defer RemoveTaskPointer(tr.workspaceManager.Root(), payload.TaskID)
		}
		if err := writeTaskReposFile(worktree, payload, env); err != nil {
			return nil, fmt.Errorf("write task repos: %w", err)
		}
		// Deterministically clone/update the delivery repo (fetch → inst → node)
		// so the agent starts on node with upstream deliverables readable via
		// inst. Best-effort: on failure the agent can still clone manually from
		// .cs-cloud.repos, so a transient git error does not fail the task. Run
		// it under a dedicated bounded context so a stalled clone/fetch cannot
		// block session startup indefinitely (the caller ctx may have no
		// deadline); the agent session ctx below is derived from the original
		// ctx, so a prepare timeout does not shorten the session itself.
		prepareCtx, prepareCancel := context.WithTimeout(ctx, deliveryRepoPrepareTimeout)
		logDeliveryRepoPrepare(prepareDeliveryRepo(prepareCtx, worktree, payload, env))
		prepareCancel()
		ctx, cancel := tr.withAgentTimeout(ctx)
		defer cancel()
		return tr.sessionRunner.RunSession(ctx, sessionID, worktree, payload.Prompt, env, SessionPermissionBypass)
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
// else a fresh per-task dir) and ensures it exists. It returns the task root
// (the agent's cwd); the agent clones any repos it needs into this dir itself
// (guided by the task prompt + env vars).
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
	env := tr.buildEnv(payload, worktree)
	if err := writeTaskEnvFile(worktree, env); err != nil {
		return nil, fmt.Errorf("write task env: %w", err)
	}
	if err := writeTaskReposFile(worktree, payload, env); err != nil {
		return nil, fmt.Errorf("write task repos: %w", err)
	}
	logDeliveryRepoPrepare(prepareDeliveryRepo(ctx, worktree, payload, env))
	cmd.Env = env

	return cmd.CombinedOutput()
}

// writeTaskEnvFile persists the CS_CLOUD_* task variables to <workdir>/.cs-cloud.env
// so in-task CLIs (cs-cloud workflow *) can read their target context from a
// file. Only CS_CLOUD_* keys are written; the rest of the process env is not
// persisted.
//
// The write goes to a temp file in the same dir and atomically renames onto the
// target. If the target is a repository-controlled symlink (checked into the
// worktree), this replaces the symlink itself instead of writing through it to
// an arbitrary path. A failure is propagated: without this file an in-task CLI
// that doesn't inherit the agent process env cannot resolve task context and
// the agent could not signal completion, so it is better to fail the task up
// front than run work that can never be reported.
func writeTaskEnvFile(workdir string, env []string) error {
	var lines []string
	for _, e := range env {
		if k, _, ok := strings.Cut(e, "="); ok && strings.HasPrefix(k, "CS_CLOUD_") && k != EnvPrompt {
			lines = append(lines, e)
		}
	}
	if len(lines) == 0 {
		return nil
	}
	data := []byte(strings.Join(lines, "\n") + "\n")
	tmp, err := os.CreateTemp(workdir, ".cs-cloud-env-*")
	if err != nil {
		return fmt.Errorf("create task env file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once rename succeeds
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write task env file: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod task env file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close task env file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(workdir, TaskEnvFileName)); err != nil {
		return fmt.Errorf("install task env file: %w", err)
	}
	return nil
}

type taskDeliverableRef struct {
	ID       string `json:"deliverable_id"`
	Title    string `json:"title"`
	Path     string `json:"path"`
	Required bool   `json:"required"`
}

func writeTaskReposFile(workdir string, payload workflow.TaskRunPayload, env []string) error {
	envMap := envSliceToMap(env)
	submittable := submittableDeliverableIDs(payload.Deliverables)
	var b strings.Builder
	b.WriteString("代码仓库：\n")
	codeCount := 0
	for _, r := range payload.Repos {
		if strings.EqualFold(r.Role, "delivery") {
			continue
		}
		codeCount++
		purpose := "按需克隆；仅在需要修改或查看该仓库时拉取。用于修改任务所属项目的业务代码，完成后提交 MR/PR。"
		if len(submittable) == 0 {
			purpose = "按需克隆；仅在需要查看该仓库时拉取。用于只读审查任务上下文，不要提交代码变更。"
		}
		writeRepoBlock(&b, r, purpose, envMap, submittable)
	}
	if codeCount == 0 {
		if repoURL := strings.TrimSpace(payload.RepoURL); repoURL != "" {
			purpose := "按需克隆；仅在需要修改或查看该仓库时拉取。用于修改任务所属项目的业务代码，完成后提交 MR/PR。"
			if len(submittable) == 0 {
				purpose = "按需克隆；仅在需要查看该仓库时拉取。用于只读审查任务上下文，不要提交代码变更。"
			}
			writeRepoBlock(&b, workflow.RepoSpec{
				URL:  repoURL,
				Role: "code",
			}, purpose, envMap, submittable)
		} else {
			b.WriteString("- 无\n")
		}
	}

	b.WriteString("\n交付物仓库：\n")
	deliveryCount := 0
	for _, r := range payload.Repos {
		if !strings.EqualFold(r.Role, "delivery") {
			continue
		}
		deliveryCount++
		purpose := "写交付文档，完成后通过 cs-cloud workflow deliverable submit 提交。"
		if len(submittable) == 0 {
			purpose = "查看交付物仓库中的既有内容和分支上下文；当前任务不应提交交付物。"
		}
		writeRepoBlock(&b, r, purpose, envMap, submittable)
	}
	if deliveryCount == 0 {
		b.WriteString("- 无\n")
	}

	b.WriteString("\n交付物：\n")
	refs := taskDeliverableRefs(envMap)
	if len(refs) == 0 {
		if len(submittable) > 0 {
			b.WriteString("- （未预设）由你根据任务自行定义交付物：产出文档后用 `cs-cloud workflow deliverable submit --file <路径> --title \"<交付物名>\"` 提交（无需预定义 id，命令会自动创建）。\n")
		} else {
			b.WriteString("- 无可提交交付物；如有交付物信息，仅作为只读审查上下文。\n")
		}
	} else {
		for _, d := range refs {
			title := strings.TrimSpace(d.Title)
			if title == "" {
				title = d.ID
			}
			fmt.Fprintf(&b, "- %s\n", title)
			if d.ID != "" {
				fmt.Fprintf(&b, "  ID：%s\n", d.ID)
			}
			if d.Required {
				b.WriteString("  必需：是\n")
			} else {
				b.WriteString("  必需：否\n")
			}
			b.WriteString("  提交类型：按任务要求选择文档文件（--file）或代码 MR/PR（--mr --repo）\n")
			if d.Path != "" {
				fmt.Fprintf(&b, "  文档写入路径（仅 --file）：%s\n", d.Path)
			}
			if d.ID != "" && d.Path != "" && submittable[d.ID] {
				fmt.Fprintf(&b, "  提交命令：在交付仓库目录内运行 cs-cloud workflow deliverable submit --deliverable %s --file %s\n", shellQuote(d.ID), shellQuote(d.Path))
			}
		}
	}
	b.WriteString("\n认证信息：在 .cs-cloud.env，由命令自动读取。不要把 token 写进回复、文档或提交内容。\n")
	b.WriteString("禁止：不要猜其他 token；不要把 token 写进回复、文档或提交内容；缺少权限时停止并请求补充。\n")
	return writeAtomicFile(workdir, TaskReposFileName, ".cs-cloud-repos-*", []byte(b.String()), 0o600)
}

func writeRepoBlock(b *strings.Builder, r workflow.RepoSpec, purpose string, env map[string]string, submittable map[string]bool) {
	label := strings.TrimSpace(r.Alias)
	if label == "" {
		label = strings.TrimSpace(r.URL)
	}
	if label == "" {
		label = "repository"
	}
	fmt.Fprintf(b, "- %s\n", label)
	if r.URL != "" {
		fmt.Fprintf(b, "  地址：%s\n", r.URL)
	}
	if provider := repoProviderLabel(r.Provider); provider != "" {
		fmt.Fprintf(b, "  类型：%s\n", provider)
	}
	if strings.EqualFold(r.Role, "delivery") {
		if node := env["CS_CLOUD_GITEA_NODE_BRANCH"]; node != "" {
			fmt.Fprintf(b, "  node 分支：%s\n", node)
		}
		if inst := env["CS_CLOUD_GITEA_INST_BRANCH"]; inst != "" {
			fmt.Fprintf(b, "  inst 分支：%s\n", inst)
		} else if r.BaseBranch != "" {
			fmt.Fprintf(b, "  inst 分支：%s\n", r.BaseBranch)
		}
		if tokenEnv := repoTokenEnv(r); tokenEnv != "" {
			fmt.Fprintf(b, "  仓库认证：使用 .cs-cloud.env 中的 %s 拉取并推送交付物仓库\n", tokenEnv)
			if cloneCmd := deliveryRepoCloneCommand(r, label, tokenEnv, env["CS_CLOUD_GITEA_NODE_BRANCH"]); cloneCmd != "" {
				fmt.Fprintf(b, "  克隆：%s\n", cloneCmd)
			}
			if updateCmd := deliveryRepoUpdateCommand(label, env["CS_CLOUD_GITEA_NODE_BRANCH"]); updateCmd != "" {
				fmt.Fprintf(b, "  更新：%s\n", updateCmd)
			}
		} else {
			b.WriteString("  仓库认证：未声明专用环境变量；不要猜 token，缺少权限时停止并请求补充。\n")
		}
		if len(submittable) > 0 {
			b.WriteString("  提交上报：cs-cloud workflow deliverable submit 使用 CS_CLOUD_TOKEN 和 CS_CLOUD_BACKEND_URL 上报交付物 PR/MR\n")
		}
	} else if r.BaseBranch != "" {
		fmt.Fprintf(b, "  基准分支：%s\n", r.BaseBranch)
	}
	if !strings.EqualFold(r.Role, "delivery") {
		if tokenEnv := repoTokenEnv(r); tokenEnv != "" {
			fmt.Fprintf(b, "  拉取认证：使用 .cs-cloud.env 中的 %s\n", tokenEnv)
			if cloneCmd := repoCloneCommand(r, label, tokenEnv); cloneCmd != "" {
				fmt.Fprintf(b, "  克隆：%s\n", cloneCmd)
			}
		} else {
			b.WriteString("  拉取认证：未声明专用环境变量；不要猜 token，缺少权限时停止并请求补充。\n")
		}
		if label != "" {
			fmt.Fprintf(b, "  更新：cd %s && git fetch origin\n", shellQuote(label))
		}
		if submitCmd := codeRepoSubmitCommand(r, env, submittable); submitCmd != "" {
			fmt.Fprintf(b, "  代码提交：仅当该交付物要求代码 MR/PR 时，在代码仓库内 commit 后运行 %s\n", submitCmd)
		}
	}
	fmt.Fprintf(b, "  用途：%s\n", purpose)
}

func repoTokenEnv(r workflow.RepoSpec) string {
	provider := strings.ToLower(strings.TrimSpace(r.Provider))
	if provider == "" {
		provider = providerFromRepoURL(r.URL)
	}
	switch provider {
	case "github":
		return "CS_CLOUD_GITHUB_TOKEN"
	case "gitlab":
		return "CS_CLOUD_GITLAB_TOKEN"
	case "gitea":
		return "CS_CLOUD_GITEA_TOKEN"
	default:
		return ""
	}
}

func providerFromRepoURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Host == "" {
		// Not an absolute URL — refuse to guess; caller falls back to
		// "no dedicated token env".
		return ""
	}
	// Match on hostname only, never the path: a URL like
	// https://example.com/github-mirror/repo must NOT be treated as
	// GitHub. Self-hosted providers (gitea.internal, gitlab.corp:3000)
	// still match because their hostname carries the provider name.
	host := strings.ToLower(u.Host)
	switch {
	case strings.Contains(host, "github"):
		return "github"
	case strings.Contains(host, "gitlab"):
		return "gitlab"
	case strings.Contains(host, "gitea"):
		return "gitea"
	default:
		return ""
	}
}

func repoCloneCommand(r workflow.RepoSpec, dir, tokenEnv string) string {
	authURL := repoAuthURLTemplate(r.URL, tokenEnv)
	if authURL == "" {
		return ""
	}
	if strings.TrimSpace(dir) == "" {
		return "git clone " + authURL
	}
	// authURL keeps its ${TOKEN} shell expansion unquoted; dir is a
	// task-controlled value (repo alias) so it gets quoted.
	return fmt.Sprintf("git clone %s %s", authURL, shellQuote(dir))
}

func deliveryRepoCloneCommand(r workflow.RepoSpec, dir, tokenEnv, branch string) string {
	authURL := repoAuthURLTemplate(r.URL, tokenEnv)
	if authURL == "" {
		return ""
	}
	if strings.TrimSpace(branch) == "" {
		return repoCloneCommand(r, dir, tokenEnv)
	}
	return fmt.Sprintf("git clone --branch %s %s %s", shellQuote(branch), authURL, shellQuote(dir))
}

func deliveryRepoUpdateCommand(dir, branch string) string {
	if strings.TrimSpace(dir) == "" || strings.TrimSpace(branch) == "" {
		return ""
	}
	quotedDir := shellQuote(dir)
	quotedBranch := shellQuote(branch)
	return fmt.Sprintf("cd %s && git fetch origin && git checkout %s && git pull --ff-only origin %s", quotedDir, quotedBranch, quotedBranch)
}

func repoAuthURLTemplate(rawURL, tokenEnv string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" || tokenEnv == "" {
		return ""
	}
	username := repoTokenUsername(u.Host)
	path := u.EscapedPath()
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	if u.Fragment != "" {
		path += "#" + u.EscapedFragment()
	}
	return fmt.Sprintf("%s://%s:${%s}@%s%s", u.Scheme, username, tokenEnv, u.Host, path)
}

func repoTokenUsername(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if splitHost, _, err := net.SplitHostPort(h); err == nil {
		h = splitHost
	}
	if h == "github.com" || strings.HasSuffix(h, ".github.com") {
		return "x-access-token"
	}
	return "oauth2"
}

// shellQuote wraps s in POSIX single quotes so task-controlled values
// (deliverable id/path, clone directory, repo URL) are safe to embed in
// the shell commands written to the task repos file. Single quotes are
// escaped via the standard '\” sequence.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func submittableDeliverableIDs(deliverables []workflow.DeliverableSpec) map[string]bool {
	out := make(map[string]bool, len(deliverables))
	for _, d := range deliverables {
		if id := strings.TrimSpace(d.ID); id != "" {
			out[id] = true
		}
	}
	return out
}

func codeRepoSubmitCommand(r workflow.RepoSpec, env map[string]string, submittable map[string]bool) string {
	if strings.TrimSpace(r.URL) == "" {
		return ""
	}
	if len(submittable) == 0 {
		return ""
	}
	repo := shellQuote(strings.TrimSpace(r.URL))
	refs := taskDeliverableRefs(env)
	var cmds []string
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ID)
		if id == "" || !submittable[id] {
			continue
		}
		cmds = append(cmds, fmt.Sprintf("cs-cloud workflow deliverable submit --deliverable %s --mr --repo %s --title '<PR title>'", shellQuote(id), repo))
	}
	// Join subsequent commands on an indented line so the "代码提交：" block
	// stays readable when more than one deliverable applies to this repo.
	return strings.Join(cmds, "\n  ")
}

func repoProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "github":
		return "Github"
	case "gitlab":
		return "Gitlab"
	case "gitea":
		return "Gitea"
	default:
		return strings.TrimSpace(provider)
	}
}

func taskDeliverableRefs(env map[string]string) []taskDeliverableRef {
	raw := strings.TrimSpace(env["CS_CLOUD_GITEA_DELIVERABLES"])
	if raw == "" {
		return nil
	}
	var refs []taskDeliverableRef
	if err := json.Unmarshal([]byte(raw), &refs); err != nil {
		return nil
	}
	return refs
}

func envSliceToMap(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			out[k] = v
		}
	}
	return out
}

func writeAtomicFile(workdir, filename, pattern string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(workdir, pattern)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(tmpName, filepath.Join(workdir, filename)); err != nil {
		return fmt.Errorf("install file: %w", err)
	}
	return nil
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
	for k, v := range tr.agentEnv {
		env = setEnv(env, k, v)
	}
	for k, v := range payload.Env {
		env = setEnv(env, k, v)
	}
	env = setEnv(env, EnvWorkspaceID, payload.WorkspaceID)
	env = setEnv(env, EnvTaskID, payload.TaskID)
	env = setEnv(env, EnvAgentID, payload.AgentID)
	env = setEnv(env, EnvPrompt, payload.Prompt)
	env = setEnv(env, EnvCSCloudWorktree, worktree)
	// CS_CLOUD_BACKEND_URL + CS_CLOUD_TOKEN so in-task CLIs (cs-cloud gitea
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
	if tr.localServerURL != "" {
		env = setEnv(env, EnvLocalServerURL, tr.localServerURL)
	}
	if tr.localServerAPIKey != "" {
		env = setEnv(env, EnvLocalServerAPIKey, tr.localServerAPIKey)
	}
	// Per-deliverable Report contracts (endpoint/body field) from the payload,
	// so the in-task CLI honors multica's Report instead of hardcoding the
	// submit path (R4). Drop any value inherited from the parent process env or
	// payload.Env first: a task with no Report contract must NOT keep a stale
	// value from a prior task, which would send reports to the wrong endpoint.
	env = unsetEnv(env, "CS_CLOUD_DELIVERABLE_REPORTS")
	if raw := deliverableReportTargetsJSON(payload.Deliverables); raw != "" {
		env = setEnv(env, "CS_CLOUD_DELIVERABLE_REPORTS", raw)
	}
	return env
}

// deliverableReportTargetsJSON serializes payload.Deliverables[].Report into
// the JSON the in-task CLI reads via CS_CLOUD_DELIVERABLE_REPORTS, so it can
// honor a non-default submit endpoint / body field per deliverable (R4).
// Returns "" when no deliverable carries a Report contract, keeping the env
// unset so the CLI falls back to its hardcoded defaults.
func deliverableReportTargetsJSON(deliverables []workflow.DeliverableSpec) string {
	type target struct {
		ID        string `json:"deliverable_id"`
		Endpoint  string `json:"endpoint"`
		BodyField string `json:"body_field"`
	}
	var targets []target
	for _, d := range deliverables {
		if d.ID == "" {
			continue
		}
		if d.Report.Endpoint == "" && d.Report.BodyField == "" {
			continue
		}
		targets = append(targets, target{
			ID:        d.ID,
			Endpoint:  d.Report.Endpoint,
			BodyField: d.Report.BodyField,
		})
	}
	if len(targets) == 0 {
		return ""
	}
	b, _ := json.Marshal(targets)
	return string(b)
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

// unsetEnv removes every entry for key from env (there is normally one, but a
// caller may have appended duplicates). Used to drop task-scoped values that
// must not leak from the parent process env or payload.Env into a task that has
// no such contract — e.g. CS_CLOUD_DELIVERABLE_REPORTS.
func unsetEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
