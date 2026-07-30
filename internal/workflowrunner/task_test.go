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
	err      error
}

func (r *fakeSessionRunner) RunSession(_ context.Context, _ string, _ string, _ string, env []string, permMode string) ([]byte, error) {
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
