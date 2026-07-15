package workflow

import (
	"testing"

	"cs-cloud/internal/workflow"
)

func TestTaskRunnerBuildEnv(t *testing.T) {
	tr := &TaskRunner{}
	env := tr.buildEnv(workflow.TaskRunPayload{
		WorkspaceID: "ws-1",
		TaskID:      "task-1",
		Agent:       "claude",
		Env: map[string]string{
			"CUSTOM_VAR": "custom-value",
		},
	}, "/tmp/ws")

	got := make(map[string]string, len(env))
	for _, e := range env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				got[e[:i]] = e[i+1:]
				break
			}
		}
	}

	if got["MULTICA_WORKSPACE_ID"] != "ws-1" {
		t.Fatalf("MULTICA_WORKSPACE_ID = %q", got["MULTICA_WORKSPACE_ID"])
	}
	if got["MULTICA_TASK_ID"] != "task-1" {
		t.Fatalf("MULTICA_TASK_ID = %q", got["MULTICA_TASK_ID"])
	}
	if got["CS_CLOUD_WORKTREE"] != "/tmp/ws" {
		t.Fatalf("CS_CLOUD_WORKTREE = %q", got["CS_CLOUD_WORKTREE"])
	}
	if got["CUSTOM_VAR"] != "custom-value" {
		t.Fatalf("CUSTOM_VAR = %q", got["CUSTOM_VAR"])
	}
}
