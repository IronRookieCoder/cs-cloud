package workflowrunner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/workflow"
)

func TestTaskRunnerBuildEnv(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("CS_CLOUD_TASK_ID", "parent-task")
	tr := &TaskRunner{}
	tr.SetAgentEnv(map[string]string{
		"COSTRICT_BASE_URL": "https://catalog.example.test",
		"CUSTOM_VAR":        "agent-value",
	})
	env := tr.buildEnv(workflow.TaskRunPayload{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		Agent:       "claude",
		Env: map[string]string{
			"CUSTOM_VAR":       "custom-value",
			"CS_CLOUD_TASK_ID": "override-task",
		},
	}, "/tmp/ws")

	got := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			got[k] = v
		}
	}

	if got["CS_CLOUD_WORKSPACE_ID"] != "ws-1" {
		t.Fatalf("CS_CLOUD_WORKSPACE_ID = %q", got["CS_CLOUD_WORKSPACE_ID"])
	}
	if got["CS_CLOUD_TASK_ID"] != "task-1" {
		t.Fatalf("CS_CLOUD_TASK_ID = %q, want task-1 (override failed)", got["CS_CLOUD_TASK_ID"])
	}
	if got["CS_CLOUD_WORKTREE"] != "/tmp/ws" {
		t.Fatalf("CS_CLOUD_WORKTREE = %q", got["CS_CLOUD_WORKTREE"])
	}
	if got["CUSTOM_VAR"] != "custom-value" {
		t.Fatalf("CUSTOM_VAR = %q", got["CUSTOM_VAR"])
	}
	if got["COSTRICT_BASE_URL"] != "https://catalog.example.test" {
		t.Fatalf("COSTRICT_BASE_URL = %q", got["COSTRICT_BASE_URL"])
	}
	if got["PATH"] != "/usr/local/bin:/usr/bin" {
		t.Fatalf("PATH not inherited: %q", got["PATH"])
	}
}

func TestTaskRunnerPassesPromptAsArgument(t *testing.T) {
	installFakeAgent(t, "fake-agent")

	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"fake-agent"})

	out, err := tr.Run(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-1",
		WorkspaceID: "ws-1",
		Agent:       "fake-agent",
		Prompt:      "hello world",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Fatalf("output = %q, want prompt as argument", out)
	}
}

func TestTaskRunnerResolvesAgentBeforeTaskEnv(t *testing.T) {
	// Install the real allowed fake agent.
	installFakeAgent(t, "fake-agent")

	// Create a second directory containing an attacker-controlled binary with
	// the same allowed name. If the runner resolves the agent after applying
	// payload.Env["PATH"], it would execute this malicious binary instead.
	attackerDir := t.TempDir()
	attackerBin := filepath.Join(attackerDir, "fake-agent")
	if err := os.WriteFile(attackerBin, []byte("#!/bin/sh\nprintf 'attacker'\n"), 0o755); err != nil {
		t.Fatalf("write attacker binary: %v", err)
	}

	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"fake-agent"})

	out, err := tr.Run(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-secure",
		WorkspaceID: "ws-1",
		Agent:       "fake-agent",
		Prompt:      "hello world",
		Env: map[string]string{
			"PATH": attackerDir,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(string(out), "attacker") {
		t.Fatalf("runner used attacker-controlled binary; output = %q", out)
	}
	if !strings.Contains(string(out), "hello world") {
		t.Fatalf("output = %q, want prompt from real fake agent", out)
	}
}

func TestTaskRunnerCscAddsOutputFormatText(t *testing.T) {
	installFakeAgent(t, "csc")

	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})

	out, err := tr.Run(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-2",
		WorkspaceID: "ws-1",
		Agent:       "csc",
		Prompt:      "do thing",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "do thing") {
		t.Fatalf("output = %q, want prompt", got)
	}
	if !strings.Contains(got, "--output-format") || !strings.Contains(got, "text") {
		t.Fatalf("output = %q, want --output-format text", got)
	}
}

type fakeSessionRunner struct {
	env      []string
	permMode string
	prompt   string
	err      error
	// onRun, when set, is invoked at the start of RunSession. Tests use it to
	// simulate the agent calling the explicit "complete task" tool so the
	// pure-tool driver treats the run as completed.
	onRun func()
}

func (r *fakeSessionRunner) RunSession(_ context.Context, _ string, _ string, prompt string, env []string, permMode string) ([]byte, error) {
	if r.onRun != nil {
		r.onRun()
	}
	r.prompt = prompt
	r.env = env
	r.permMode = permMode
	return []byte("session runner used"), r.err
}

func TestTaskRunnerCscSessionUsesBoundSessionWithTaskEnv(t *testing.T) {
	installFakeAgent(t, "csc")

	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	runner := &fakeSessionRunner{}
	tr.SetSessionRunner(runner)

	out, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-env",
		WorkspaceID: "ws-1",
		Agent:       "csc",
		Prompt:      "do thing",
		Env: map[string]string{
			"FAKE_AGENT_PRINT_ENV": "CS_CLOUD_NODE_RUN_ID",
			"CS_CLOUD_NODE_RUN_ID": "nr-env",
		},
	}, t.TempDir(), "session-1")
	if err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}
	got := string(out)
	if !strings.Contains(got, "session runner used") {
		t.Fatalf("RunCSCSession did not use bound session runner; output = %q", got)
	}
	env := map[string]string{}
	for _, e := range runner.env {
		if k, v, ok := strings.Cut(e, "="); ok {
			env[k] = v
		}
	}
	if env["CS_CLOUD_NODE_RUN_ID"] != "nr-env" {
		t.Fatalf("CS_CLOUD_NODE_RUN_ID = %q, want nr-env", env["CS_CLOUD_NODE_RUN_ID"])
	}
}

// TestRunCSCSession_InjectsTaskIDIntoPrompt verifies RunCSCSession passes the
// session runner a prompt carrying the task id, so the agent can pass
// `--task <id>` to cs-cloud workflow commands and have them locate .cs-cloud.env
// regardless of cwd.
func TestRunCSCSession_InjectsTaskIDIntoPrompt(t *testing.T) {
	installFakeAgent(t, "csc")

	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	runner := &fakeSessionRunner{}
	tr.SetSessionRunner(runner)

	workdir := t.TempDir()
	const taskID = "task-prompt"
	if _, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID: taskID, WorkspaceID: "ws-1", Agent: "csc", Prompt: "do thing",
	}, workdir, "session-1"); err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}
	if !strings.Contains(runner.prompt, taskID) {
		t.Fatalf("session prompt must carry the task id %q so the agent can pass --task; got:\n%s", taskID, runner.prompt)
	}
	if !strings.Contains(runner.prompt, "do thing") {
		t.Fatalf("session prompt must preserve the original body; got:\n%s", runner.prompt)
	}
}

// TestRunCSCSession_WritesAndRemovesTaskPointer verifies RunCSCSession writes a
// task pointer (<runs>/<taskID> → worktree) the in-task CLI can read in O(1),
// then removes it when the session returns. The pointer is observed from the
// session runner's onRun callback (fired while the session — and the pointer —
// are live), because the defer removes it before RunCSCSession returns.
func TestRunCSCSession_WritesAndRemovesTaskPointer(t *testing.T) {
	installFakeAgent(t, "csc")

	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	var seenDuring string
	runner := &fakeSessionRunner{onRun: func() {
		seenDuring = ReadTaskPointer(wm.Root(), "task-ptr")
	}}
	tr.SetSessionRunner(runner)

	workdir := t.TempDir()
	if _, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID: "task-ptr", WorkspaceID: "ws-1", Agent: "csc", Prompt: "do",
	}, workdir, "sess-1"); err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}
	if seenDuring != workdir {
		t.Fatalf("pointer during session = %q, want %q", seenDuring, workdir)
	}
	if got := ReadTaskPointer(wm.Root(), "task-ptr"); got != "" {
		t.Fatalf("pointer after session = %q, want empty (removed by defer)", got)
	}
}

// TestInjectTaskEnvGuidance verifies cs-cloud prepends a "Task Environment"
// section naming the task id and telling the agent to pass `--task <id>` (a
// short id, not a long path), and to retry when the output says not delivered.
func TestInjectTaskEnvGuidance(t *testing.T) {
	const taskID = "task-9"
	const body = "Issue: ship the thing\n\nDo work."
	got := injectTaskEnvGuidance(body, taskID)

	// Guidance is prepended before the original prompt body.
	idxGuide := strings.Index(got, "Task Environment")
	idxBody := strings.Index(got, body)
	if idxGuide < 0 || idxBody < 0 || idxGuide >= idxBody {
		t.Fatalf("guidance must be prepended before the original body; got:\n%s", got)
	}
	// Names the task id.
	if !strings.Contains(got, taskID) {
		t.Fatalf("guidance must name the task id %q; got:\n%s", taskID, got)
	}
	// Tells the agent to pass --task <id> (NOT --task-root / a path).
	if !strings.Contains(got, "--task") || strings.Contains(got, "--task-root") {
		t.Fatalf("guidance must use `--task <id>`, not --task-root; got:\n%s", got)
	}
	// References the completion signal and retry-when-not-delivered.
	if !strings.Contains(got, "task complete") {
		t.Fatalf("guidance must reference `task complete`; got:\n%s", got)
	}
	if !strings.Contains(got, "NOT delivered") && !strings.Contains(got, "not delivered") {
		t.Fatalf("guidance must tell the agent to retry when the output says not delivered; got:\n%s", got)
	}
}

// TestRunCSCSession_WritesTaskEnvFile verifies a csc task run writes the
// CS_CLOUD_* context to .cs-cloud.env in the workdir, so the in-task CLI can
// read its target context from a file (not just process env).
func TestRunCSCSession_WritesTaskEnvFile(t *testing.T) {
	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	tr.SetSessionRunner(&fakeSessionRunner{})
	tr.SetLocalServerURL("http://127.0.0.1:9999")

	workdir := t.TempDir()
	if _, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID: "task-envfile", WorkspaceID: "ws-1", Agent: "csc", Prompt: "do",
	}, workdir, "sess-1"); err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(workdir, TaskEnvFileName))
	if err != nil {
		t.Fatalf(".cs-cloud.env not written to workdir: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "CS_CLOUD_TASK_ID=task-envfile") {
		t.Errorf("env file missing CS_CLOUD_TASK_ID:\n%s", got)
	}
	if !strings.Contains(got, "CS_CLOUD_LOCAL_URL=http://127.0.0.1:9999") {
		t.Errorf("env file missing CS_CLOUD_LOCAL_URL:\n%s", got)
	}
	if !strings.Contains(got, "CS_CLOUD_WORKSPACE_ID=ws-1") {
		t.Errorf("env file missing CS_CLOUD_WORKSPACE_ID:\n%s", got)
	}
}

func TestRunCSCSession_WritesTaskReposFile(t *testing.T) {
	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	tr.SetSessionRunner(&fakeSessionRunner{})

	workdir := t.TempDir()
	if _, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-repos",
		WorkspaceID: "ws-1",
		Agent:       "csc",
		Prompt:      "do",
		Env: map[string]string{
			"CS_CLOUD_GITLAB_TOKEN":       "gitlab-secret",
			"CS_CLOUD_GITEA_TOKEN":        "gitea-secret",
			"CS_CLOUD_GITEA_CLONE_URL":    "https://gitea.test/t/wf.git",
			"CS_CLOUD_GITEA_INST_BRANCH":  "inst-1",
			"CS_CLOUD_GITEA_NODE_BRANCH":  "node-1",
			"CS_CLOUD_GITEA_DELIVERABLES": `[{"deliverable_id":"d1","title":"Design","path":"nodes/design.md","required":true}]`,
		},
		Repos: []workflow.RepoSpec{
			{URL: "https://gitlab.test/root/demo.git", Provider: "gitlab", Role: "code", Alias: "demo", BaseBranch: "main"},
			{URL: "https://gitea.test/t/wf.git", Provider: "gitea", Role: "delivery", Alias: "delivery", BaseBranch: "inst-1", BotToken: "gitea-secret"},
		},
		Deliverables: []workflow.DeliverableSpec{
			{ID: "d1"},
		},
	}, workdir, "sess-1"); err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(workdir, TaskReposFileName))
	if err != nil {
		t.Fatalf(".cs-cloud.repos not written to workdir: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"代码仓库：",
		"- demo",
		"地址：https://gitlab.test/root/demo.git",
		"类型：Gitlab",
		"拉取认证：使用 .cs-cloud.env 中的 CS_CLOUD_GITLAB_TOKEN",
		"克隆：git clone https://oauth2:${CS_CLOUD_GITLAB_TOKEN}@gitlab.test/root/demo.git 'demo'",
		"更新：cd 'demo' && git fetch origin",
		"代码提交：仅当该交付物要求代码 MR/PR 时，在代码仓库内 commit 后运行 cs-cloud workflow deliverable submit --deliverable 'd1' --mr --repo 'https://gitlab.test/root/demo.git' --title '<PR title>'",
		"用途：按需克隆；仅在需要修改或查看该仓库时拉取。用于修改任务所属项目的业务代码，完成后提交 MR/PR。",
		"交付物仓库：",
		"- delivery",
		"类型：Gitea",
		"node 分支：node-1",
		"inst 分支：inst-1",
		"仓库认证：使用 .cs-cloud.env 中的 CS_CLOUD_GITEA_TOKEN 拉取并推送交付物仓库",
		"克隆：git clone --branch 'node-1' https://oauth2:${CS_CLOUD_GITEA_TOKEN}@gitea.test/t/wf.git 'delivery'",
		"更新：cd 'delivery' && git fetch origin && git checkout 'node-1' && git pull --ff-only origin 'node-1'",
		"提交上报：cs-cloud workflow deliverable submit 使用 CS_CLOUD_TOKEN 和 CS_CLOUD_BACKEND_URL 上报交付物 PR/MR",
		"提交命令：在交付仓库目录内运行 cs-cloud workflow deliverable submit --deliverable 'd1' --file 'nodes/design.md'",
		"禁止：不要猜其他 token；不要把 token 写进回复、文档或提交内容；缺少权限时停止并请求补充。",
		"交付物：",
		"ID：d1",
		"必需：是",
		"提交类型：按任务要求选择文档文件（--file）或代码 MR/PR（--mr --repo）",
		"文档写入路径（仅 --file）：nodes/design.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf(".cs-cloud.repos missing %q:\n%s", want, got)
		}
	}
	for _, secret := range []string{"gitlab-secret", "gitea-secret", "CLONE_URL_AUTHED"} {
		if strings.Contains(got, secret) {
			t.Errorf(".cs-cloud.repos leaked secret %q:\n%s", secret, got)
		}
	}
}

func TestRunCSCSession_WritesTaskReposFileReadOnlyDeliverablesDoNotAdvertiseSubmit(t *testing.T) {
	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	tr.SetSessionRunner(&fakeSessionRunner{})

	workdir := t.TempDir()
	if _, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-readonly-repos",
		WorkspaceID: "ws-1",
		Agent:       "csc",
		Prompt:      "review",
		Env: map[string]string{
			"CS_CLOUD_GITLAB_TOKEN":       "gitlab-secret",
			"CS_CLOUD_GITEA_TOKEN":        "gitea-secret",
			"CS_CLOUD_GITEA_CLONE_URL":    "https://gitea.test/t/wf.git",
			"CS_CLOUD_GITEA_INST_BRANCH":  "inst-1",
			"CS_CLOUD_GITEA_NODE_BRANCH":  "node-1",
			"CS_CLOUD_GITEA_DELIVERABLES": `[{"deliverable_id":"d1","title":"Design","path":"nodes/design.md","required":true}]`,
		},
		Repos: []workflow.RepoSpec{
			{URL: "https://gitlab.test/root/demo.git", Provider: "gitlab", Role: "code", Alias: "demo", BaseBranch: "main"},
			{URL: "https://gitea.test/t/wf.git", Provider: "gitea", Role: "delivery", Alias: "delivery", BaseBranch: "inst-1", BotToken: "gitea-secret"},
		},
	}, workdir, "sess-1"); err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(workdir, TaskReposFileName))
	if err != nil {
		t.Fatalf(".cs-cloud.repos not written to workdir: %v", err)
	}
	got := string(b)
	for _, want := range []string{
		"https://gitlab.test/root/demo.git",
		"https://gitea.test/t/wf.git",
		"d1",
		"nodes/design.md",
	} {
		if !strings.Contains(got, want) {
			t.Errorf(".cs-cloud.repos missing read-only context %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{
		"workflow deliverable submit",
		"代码提交",
		"提交命令",
		"自行定义交付物",
	} {
		if strings.Contains(got, forbidden) {
			t.Errorf(".cs-cloud.repos advertised submit instruction %q for read-only deliverables:\n%s", forbidden, got)
		}
	}
}

func TestRunCSCSession_WritesLegacyRepoURLToTaskReposFile(t *testing.T) {
	wm := NewWorkspaceManager(t.TempDir())
	tr := NewTaskRunner(wm, time.Minute, []string{"csc"})
	tr.SetSessionRunner(&fakeSessionRunner{})

	workdir := t.TempDir()
	if _, err := tr.RunCSCSession(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-legacy-repo",
		WorkspaceID: "ws-1",
		Agent:       "csc",
		Prompt:      "do",
		RepoURL:     "https://gitlab.test/root/legacy.git",
	}, workdir, "sess-1"); err != nil {
		t.Fatalf("RunCSCSession: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(workdir, TaskReposFileName))
	if err != nil {
		t.Fatalf(".cs-cloud.repos not written to workdir: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "https://gitlab.test/root/legacy.git") {
		t.Fatalf(".cs-cloud.repos missing legacy RepoURL:\n%s", got)
	}
	if strings.Contains(got, "- 鏃?") {
		t.Fatalf(".cs-cloud.repos reported no code repos despite legacy RepoURL:\n%s", got)
	}
	if strings.Contains(got, "workflow deliverable submit") || strings.Contains(got, "MR/PR") {
		t.Fatalf(".cs-cloud.repos advertised submit instructions for legacy RepoURL with no deliverables:\n%s", got)
	}
	if !strings.Contains(got, "不要提交代码变更") {
		t.Fatalf(".cs-cloud.repos missing read-only purpose for legacy RepoURL with no deliverables:\n%s", got)
	}
}

// TestBuildEnvInjectsLocalServerURL verifies the in-task env carries the
// localserver URL so the "complete task" CLI can call back into this device's
// /workflow/tasks/{id}/complete endpoint.
func TestBuildEnvInjectsLocalServerURL(t *testing.T) {
	tr := NewTaskRunner(NewWorkspaceManager(t.TempDir()), time.Minute, []string{"csc"})
	tr.SetLocalServerURL("http://127.0.0.1:9999")

	env := tr.buildEnv(workflow.TaskRunPayload{TaskID: "t1", WorkspaceID: "ws-1"}, t.TempDir())
	got := ""
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok && k == "CS_CLOUD_LOCAL_URL" {
			got = v
		}
	}
	if got != "http://127.0.0.1:9999" {
		t.Fatalf("CS_CLOUD_LOCAL_URL = %q, want http://127.0.0.1:9999", got)
	}
}

// TestWriteTaskEnvFile_PersistsOnlyCSCloudVars verifies the task context is
// written to .cs-cloud.env (only CS_CLOUD_* keys), so in-task CLIs can read it
// from a file instead of relying on env propagation.
func TestWriteTaskEnvFile_PersistsOnlyCSCloudVars(t *testing.T) {
	dir := t.TempDir()
	env := []string{
		"PATH=/usr/bin",
		"CS_CLOUD_TASK_ID=task-xyz",
		"CS_CLOUD_PROMPT=first line\nsecond line that is prompt text",
		"CS_CLOUD_LOCAL_URL=http://127.0.0.1:5000",
		"OTHER_VAR=skip-me",
	}
	writeTaskEnvFile(dir, env)

	b, err := os.ReadFile(filepath.Join(dir, TaskEnvFileName))
	if err != nil {
		t.Fatalf("read env file: %v", err)
	}
	got := string(b)
	if !strings.Contains(got, "CS_CLOUD_TASK_ID=task-xyz") {
		t.Errorf("missing CS_CLOUD_TASK_ID: %s", got)
	}
	if !strings.Contains(got, "CS_CLOUD_LOCAL_URL=http://127.0.0.1:5000") {
		t.Errorf("missing CS_CLOUD_LOCAL_URL: %s", got)
	}
	if strings.Contains(got, "CS_CLOUD_PROMPT=") || strings.Contains(got, "second line that is prompt text") {
		t.Errorf("env file should not persist CS_CLOUD_PROMPT or prompt body: %s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if line == "" || strings.HasPrefix(line, "CS_CLOUD_") {
			continue
		}
		t.Errorf("non-CS_CLOUD var leaked into env file: %q", line)
	}
}

// TestProviderFromRepoURL_HostOnly locks in the host-only match: a URL whose
// path contains a provider name must not be misclassified, while self-hosted
// hostnames (gitea.corp, gitlab.example.com) still resolve.
func TestProviderFromRepoURL_HostOnly(t *testing.T) {
	cases := []struct {
		url  string
		want string
	}{
		{"https://github.com/o/r.git", "github"},
		{"https://gitlab.example.com/g/r.git", "gitlab"},
		{"https://gitea.corp:3000/t/r.git", "gitea"},
		{"https://example.com/github-mirror/r.git", ""},
		{"https://example.com/gitlab-fork/r.git", ""},
		{"https://example.com/gitea-copy/r.git", ""},
		{"10.20.19.101:33000/t/r.git", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := providerFromRepoURL(tc.url); got != tc.want {
			t.Errorf("providerFromRepoURL(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestShellQuote verifies POSIX single-quote escaping for task-controlled
// values embedded into the shell commands written to the task repos file.
func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"demo", "'demo'"},
		{"a b", "'a b'"},
		{"a'b", "'a'\\''b'"},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"foo;bar", "'foo;bar'"},
		{"d1", "'d1'"},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCodeRepoSubmitCommand_AllDeliverables verifies a submit command is
// emitted for every deliverable reference, not just the first.
func TestCodeRepoSubmitCommand_AllDeliverables(t *testing.T) {
	env := map[string]string{
		"CS_CLOUD_GITEA_DELIVERABLES": `[{"deliverable_id":"d1","path":"a.md"},{"deliverable_id":"d2","path":"b.md"}]`,
	}
	submittable := map[string]bool{"d1": true, "d2": true}
	got := codeRepoSubmitCommand(workflow.RepoSpec{URL: "https://gitlab.test/o/r.git"}, env, submittable)
	if !strings.Contains(got, "--deliverable 'd1'") || !strings.Contains(got, "--deliverable 'd2'") {
		t.Errorf("expected submit commands for both deliverables, got:\n%s", got)
	}
	if !strings.Contains(got, "--title '<PR title>'") {
		t.Errorf("expected submit command to remind agent to provide a PR title, got:\n%s", got)
	}
	// empty repo URL -> no command
	if got := codeRepoSubmitCommand(workflow.RepoSpec{}, env, submittable); got != "" {
		t.Errorf("expected empty submit command when repo URL is empty, got %q", got)
	}
	// no deliverables -> empty
	if got := codeRepoSubmitCommand(workflow.RepoSpec{URL: "https://x/y.git"}, map[string]string{}, submittable); got != "" {
		t.Errorf("expected empty submit command when no deliverables, got %q", got)
	}
	// read-only context: env may list deliverables, but payload did not declare
	// submittable deliverables.
	if got := codeRepoSubmitCommand(workflow.RepoSpec{URL: "https://x/y.git"}, env, nil); got != "" {
		t.Errorf("expected empty submit command for read-only deliverable context, got %q", got)
	}
}

func TestPrepare_TaskRootFresh(t *testing.T) {
	requireGit(t)
	installFakeAgent(t, AgentCsc) // make exec.LookPath("csc") resolve
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(), AllowedAgents: []string{AgentCsc},
	}
	wm := NewWorkspaceManager(cfg.WorkspacesRoot)
	tr := NewTaskRunner(wm, 0, cfg.AllowedAgents)

	worktree, _, err := tr.Prepare(context.Background(), workflow.TaskRunPayload{
		TaskID: "11111111-aaaa-bbbb-cccc-dddddddddddd", WorkspaceID: "ws-1", Agent: AgentCsc,
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	want := filepath.Join(cfg.WorkspacesRoot, "ws-1", "tasks", "11111111-aaaa-bbbb-cccc-dddddddddddd")
	if worktree != want {
		t.Errorf("taskRoot = %q, want %q", worktree, want)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Errorf("taskRoot not created: %v", err)
	}
}

func TestPrepare_PriorWorkDirReused(t *testing.T) {
	requireGit(t)
	installFakeAgent(t, AgentCsc)
	root := t.TempDir()
	prior := filepath.Join(root, "ws-1", "tasks", "prior-task")
	_ = os.MkdirAll(prior, 0o755)
	wm := NewWorkspaceManager(root)
	tr := NewTaskRunner(wm, 0, []string{AgentCsc})

	worktree, _, err := tr.Prepare(context.Background(), workflow.TaskRunPayload{
		TaskID: "new-task-id", WorkspaceID: "ws-1", Agent: AgentCsc, PriorWorkDir: prior,
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if worktree != prior {
		t.Errorf("taskRoot = %q, want reuse prior %q", worktree, prior)
	}
}

func TestPrepare_PriorWorkDirMissingFallsBackToFresh(t *testing.T) {
	requireGit(t)
	installFakeAgent(t, AgentCsc)
	root := t.TempDir()
	wm := NewWorkspaceManager(root)
	tr := NewTaskRunner(wm, 0, []string{AgentCsc})

	// PriorWorkDir set but does NOT exist on disk (e.g. GC'd, or different device).
	missingPrior := filepath.Join(root, "ws-1", "tasks", "gone-task")
	worktree, _, err := tr.Prepare(context.Background(), workflow.TaskRunPayload{
		TaskID: "new-task-id", WorkspaceID: "ws-1", Agent: AgentCsc, PriorWorkDir: missingPrior,
	})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	want := filepath.Join(root, "ws-1", "tasks", "new-task-id") // TaskWorktreeDir
	if worktree != want {
		t.Errorf("taskRoot = %q, want fresh fallback %q", worktree, want)
	}
	if _, err := os.Stat(worktree); err != nil {
		t.Errorf("fresh taskRoot not created: %v", err)
	}
}
