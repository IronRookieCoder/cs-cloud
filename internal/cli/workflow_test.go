package cli

import (
	"io"
	"os"
	"path/filepath"
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

// TestLoadTaskEnvFile_PopulatesProcessEnv verifies `cs-cloud workflow` fills
// missing CS_CLOUD_* values from .cs-cloud.env in the workdir (cwd) so CLIs
// resolve task context when env propagation through the agent subprocess fails.
func TestLoadTaskEnvFile_PopulatesProcessEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Unsetenv("CS_CLOUD_TASK_ID")
	os.Unsetenv("CS_CLOUD_LOCAL_URL")

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

// TestLoadTaskEnvFile_KeepsProcessEnvAuthoritative verifies a stale .cs-cloud.env
// in a resumed session's cwd cannot override the live task ID injected by the
// daemon. Only keys not already present in the process env are filled from the
// file.
func TestLoadTaskEnvFile_KeepsProcessEnvAuthoritative(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CS_CLOUD_TASK_ID", "new-task")
	os.Unsetenv("CS_CLOUD_LOCAL_URL")

	content := "CS_CLOUD_TASK_ID=old-task\nCS_CLOUD_LOCAL_URL=http://127.0.0.1:9\n"
	if err := os.WriteFile(workflowrunner.TaskEnvFileName, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	loadTaskEnvFile()

	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "new-task" {
		t.Errorf("CS_CLOUD_TASK_ID = %q, want new-task (process env must stay authoritative)", got)
	}
	if got := os.Getenv("CS_CLOUD_LOCAL_URL"); got != "http://127.0.0.1:9" {
		t.Errorf("CS_CLOUD_LOCAL_URL = %q, want http://127.0.0.1:9 (missing key filled from file)", got)
	}
}

// TestLoadTaskEnvFile_WalksUpToTaskRoot verifies the env file is resolved even
// when the agent runs in-task CLIs from a cloned repo subdir (the submit prompt
// tells it to cd into the delivery repo). The file lives in the task root.
func TestLoadTaskEnvFile_WalksUpToTaskRoot(t *testing.T) {
	root := t.TempDir()
	content := "CS_CLOUD_TASK_ID=from-root\n"
	if err := os.WriteFile(filepath.Join(root, workflowrunner.TaskEnvFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	// Agent is several levels deep inside a cloned repo under the task root.
	sub := filepath.Join(root, "wf-repo", "nodes", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	t.Chdir(sub)
	os.Unsetenv("CS_CLOUD_TASK_ID")

	loadTaskEnvFile()

	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "from-root" {
		t.Errorf("CS_CLOUD_TASK_ID = %q, want from-root (loaded via upward walk from subdir)", got)
	}
}

// TestLoadTaskEnvFile_NoFileIsNoOp verifies a missing file (e.g. cwd is outside
// any task tree) is a silent no-op, not an error.
func TestLoadTaskEnvFile_NoFileIsNoOp(t *testing.T) {
	t.Chdir(t.TempDir())
	os.Unsetenv("CS_CLOUD_TASK_ID")
	loadTaskEnvFile()
	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "" {
		t.Errorf("CS_CLOUD_TASK_ID = %q, want empty (no env file present)", got)
	}
}

func TestFindTaskEnvFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "outside.env")
	if err := os.WriteFile(target, []byte("CS_CLOUD_TASK_ID=from-symlink\n"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(dir, workflowrunner.TaskEnvFileName)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not supported on this host: %v", err)
	}
	if got := findTaskEnvFile(dir); got != "" {
		t.Fatalf("findTaskEnvFile returned symlink %q, want empty", got)
	}
}

func TestLoadTaskEnvFileIgnoresOversizedFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	os.Unsetenv("CS_CLOUD_TASK_ID")
	oversized := strings.Repeat("A", maxTaskEnvFileBytes+1)
	if err := os.WriteFile(workflowrunner.TaskEnvFileName, []byte("CS_CLOUD_TASK_ID="+oversized+"\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	loadTaskEnvFile()
	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "" {
		t.Fatalf("CS_CLOUD_TASK_ID = %q, want empty for oversized env file", got)
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
