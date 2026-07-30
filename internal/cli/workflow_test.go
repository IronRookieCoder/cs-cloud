package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"cs-cloud/internal/app"
	"cs-cloud/internal/platform"
	"cs-cloud/internal/workflowrunner"
)

func TestWorkflowWorkspaceListEmptyCache(t *testing.T) {
	platform.SetDataDir(t.TempDir())
	t.Setenv("COSTRICT_BASE_URL", "https://example.costrict.local")
	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if err := workflowWorkspaceList(a); err != nil {
		t.Fatalf("workflowWorkspaceList: %v", err)
	}
}

// TestLoadTaskEnvFile_PopulatesProcessEnv verifies `cs-cloud workflow` reads
// .cs-cloud.env from the workdir (cwd) so CLIs resolve task context from a file
// instead of relying on env propagation through the agent subprocess.
func TestLoadTaskEnvFile_PopulatesProcessEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Unsetenv("CS_CLOUD_TASK_ID")
	os.Unsetenv("CS_CLOUD_LOCAL_URL")
	t.Cleanup(func() {
		os.Unsetenv("CS_CLOUD_TASK_ID")
		os.Unsetenv("CS_CLOUD_LOCAL_URL")
	})

	content := "CS_CLOUD_TASK_ID=from-file\n# a comment, skip\nCS_CLOUD_LOCAL_URL=http://127.0.0.1:9\n"
	if err := os.WriteFile(workflowrunner.TaskEnvFileName, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	loadTaskEnvFile()

	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "from-file" {
		t.Errorf("CS_CLOUD_TASK_ID = %q, want from-file", got)
	}
	if got := os.Getenv("CS_CLOUD_LOCAL_URL"); got != "http://127.0.0.1:9" {
		t.Errorf("CS_CLOUD_LOCAL_URL = %q, want http://127.0.0.1:9", got)
	}
}

func TestPrintWorkflowUsageListsImplementedResources(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	printWorkflowUsage()
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	os.Stdout = orig
	t.Cleanup(func() { os.Stdout = orig })
	outBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	out := string(outBytes)

	if !strings.Contains(out, "workspace:") {
		t.Fatalf("workflow usage missing workspace resource:\n%s", out)
	}
	if !strings.Contains(out, "project:") {
		t.Fatalf("workflow usage missing project resource:\n%s", out)
	}
	if strings.Contains(out, "issue:") {
		t.Fatalf("workflow usage should not list removed issue resource:\n%s", out)
	}
	if !strings.Contains(out, "deliverable:") {
		t.Fatalf("workflow usage missing deliverable resource:\n%s", out)
	}
	if !strings.Contains(out, "task:") {
		t.Fatalf("workflow usage missing task resource:\n%s", out)
	}
}
