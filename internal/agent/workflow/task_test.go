package workflow

import (
	"context"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/workflow"
)

func TestTaskRunnerBuildEnv(t *testing.T) {
	t.Setenv("PATH", "/usr/local/bin:/usr/bin")
	t.Setenv("MULTICA_TASK_ID", "parent-task")
	tr := &TaskRunner{}
	env := tr.buildEnv(workflow.TaskRunPayload{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		Agent:       "claude",
		Env: map[string]string{
			"CUSTOM_VAR":      "custom-value",
			"MULTICA_TASK_ID": "override-task",
		},
	}, "/tmp/ws")

	got := make(map[string]string, len(env))
	for _, e := range env {
		if k, v, ok := strings.Cut(e, "="); ok {
			got[k] = v
		}
	}

	if got["MULTICA_WORKSPACE_ID"] != "ws-1" {
		t.Fatalf("MULTICA_WORKSPACE_ID = %q", got["MULTICA_WORKSPACE_ID"])
	}
	if got["MULTICA_TASK_ID"] != "task-1" {
		t.Fatalf("MULTICA_TASK_ID = %q, want task-1 (override failed)", got["MULTICA_TASK_ID"])
	}
	if got["CS_CLOUD_WORKTREE"] != "/tmp/ws" {
		t.Fatalf("CS_CLOUD_WORKTREE = %q", got["CS_CLOUD_WORKTREE"])
	}
	if got["CUSTOM_VAR"] != "custom-value" {
		t.Fatalf("CUSTOM_VAR = %q", got["CUSTOM_VAR"])
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
