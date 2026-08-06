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

// TestLoadTaskEnvFile_PopulatesProcessEnv verifies `cs-cloud workflow` reads
// .cs-cloud.env from the workdir (cwd) so CLIs resolve task context from a file
// instead of relying on env propagation through the agent subprocess.
func TestLoadTaskEnvFile_PopulatesProcessEnv(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CS_CLOUD_TASK_ID", "")
	t.Setenv("CS_CLOUD_LOCAL_URL", "")

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
	t.Setenv("CS_CLOUD_TASK_ID", "")

	loadTaskEnvFile()

	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "from-root" {
		t.Errorf("CS_CLOUD_TASK_ID = %q, want from-root (loaded via upward walk from subdir)", got)
	}
}

// TestLoadTaskEnvFile_NoFileIsNoOp verifies a missing file (e.g. cwd is outside
// any task tree) is a silent no-op, not an error.
func TestLoadTaskEnvFile_NoFileIsNoOp(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("CS_CLOUD_TASK_ID", "")
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

// TestPopTaskFlag verifies --task <id> is extracted from any position in the
// args (both `--task <id>` and `--task=<id>`) and removed so it does not leak
// into subcommand parsing. Absent → "".
func TestPopTaskFlag(t *testing.T) {
	// --task <id> form, mid-args.
	rem, id := popTaskFlag([]string{"task", "--task", "T-123", "complete", "--summary", "x"})
	if id != "T-123" {
		t.Fatalf("id = %q, want T-123", id)
	}
	if got, want := strings.Join(rem, " "), "task complete --summary x"; got != want {
		t.Fatalf("remaining = %q, want %q", got, want)
	}

	// --task=<id> form, leading.
	rem, id = popTaskFlag([]string{"--task=T-456", "task", "complete"})
	if id != "T-456" {
		t.Fatalf("id = %q, want T-456", id)
	}
	if got, want := strings.Join(rem, " "), "task complete"; got != want {
		t.Fatalf("remaining = %q, want %q", got, want)
	}

	// Absent — args untouched, id empty.
	rem, id = popTaskFlag([]string{"task", "complete"})
	if id != "" {
		t.Fatalf("id = %q, want empty", id)
	}
	if got, want := strings.Join(rem, " "), "task complete"; got != want {
		t.Fatalf("remaining = %q, want %q", got, want)
	}
}

// TestTaskIDFromEnvFile verifies CS_CLOUD_TASK_ID is read from a .cs-cloud.env
// file; a missing key or unreadable file yields "".
func TestTaskIDFromEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, workflowrunner.TaskEnvFileName)
	if err := os.WriteFile(path, []byte("# comment\nCS_CLOUD_TASK_ID=T-999\nCS_CLOUD_LOCAL_URL=http://x\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := taskIDFromEnvFile(path); got != "T-999" {
		t.Fatalf("taskIDFromEnvFile = %q, want T-999", got)
	}
	// File without the key.
	other := filepath.Join(dir, "other.env")
	if err := os.WriteFile(other, []byte("CS_CLOUD_LOCAL_URL=http://x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := taskIDFromEnvFile(other); got != "" {
		t.Fatalf("taskIDFromEnvFile = %q, want empty (no task id)", got)
	}
	// Missing file.
	if got := taskIDFromEnvFile(filepath.Join(dir, "nope.env")); got != "" {
		t.Fatalf("taskIDFromEnvFile = %q, want empty (missing file)", got)
	}
}

// TestFindTaskRootByTaskID verifies the scan locates the task root whose
// .cs-cloud.env carries the given CS_CLOUD_TASK_ID — even when the directory
// name is NOT the task ID. That is the rework/resume case: the runner reuses a
// prior round's directory but writes the CURRENT task id into .cs-cloud.env, so
// locating by content (not by dir name) is what makes --task <id> sound.
func TestFindTaskRootByTaskID(t *testing.T) {
	root := t.TempDir()
	// Rework simulation: dir named after a PREVIOUS task id; env carries CURRENT.
	dir := filepath.Join(root, "ws-1", "tasks", "prev-task-id")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, workflowrunner.TaskEnvFileName), []byte("CS_CLOUD_TASK_ID=curr-task-id\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// A decoy task dir with a different id.
	decoy := filepath.Join(root, "ws-1", "tasks", "other")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, workflowrunner.TaskEnvFileName), []byte("CS_CLOUD_TASK_ID=someone-else\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := findTaskRootByTaskID(root, "curr-task-id"); got != dir {
		t.Fatalf("findTaskRootByTaskID = %q, want %q", got, dir)
	}
	if got := findTaskRootByTaskID(root, "missing"); got != "" {
		t.Fatalf("findTaskRootByTaskID = %q, want empty (no match)", got)
	}
}

// TestFindTaskRootByTaskID_PrefersPointer verifies the O(1) runner-written
// pointer wins over the scan when present and its .cs-cloud.env still exists.
func TestFindTaskRootByTaskID_PrefersPointer(t *testing.T) {
	root := t.TempDir()
	// Real task root with .cs-cloud.env carrying the task id.
	realDir := filepath.Join(root, "ws-1", "tasks", "any-name")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, workflowrunner.TaskEnvFileName), []byte("CS_CLOUD_TASK_ID=T-1\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	// Runner writes the pointer → realDir.
	if err := workflowrunner.WriteTaskPointer(root, "T-1", realDir); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
	if got := findTaskRootByTaskID(root, "T-1"); got != realDir {
		t.Fatalf("findTaskRootByTaskID = %q, want %q (pointer should win)", got, realDir)
	}
}

// TestFindTaskRootByTaskID_StalePointerFallsBackToScan verifies a stale pointer
// (target dir has no .cs-cloud.env, e.g. workdir GC'd but pointer leaked from a
// crash) falls through to the scan.
func TestFindTaskRootByTaskID_StalePointerFallsBackToScan(t *testing.T) {
	root := t.TempDir()
	// Stale pointer → a dir with no .cs-cloud.env.
	if err := workflowrunner.WriteTaskPointer(root, "T-2", filepath.Join(root, "gone")); err != nil {
		t.Fatalf("write pointer: %v", err)
	}
	// The real dir the scan should find.
	realDir := filepath.Join(root, "ws-1", "tasks", "live")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, workflowrunner.TaskEnvFileName), []byte("CS_CLOUD_TASK_ID=T-2\n"), 0o600); err != nil {
		t.Fatalf("write env: %v", err)
	}
	if got := findTaskRootByTaskID(root, "T-2"); got != realDir {
		t.Fatalf("findTaskRootByTaskID = %q, want %q (scan fallback after stale pointer)", got, realDir)
	}
}

// TestLoadTaskEnvFileFrom_ExplicitDir verifies that when the agent passes an
// explicit task-root path, cs-cloud reads .cs-cloud.env from there even though
// the process cwd is somewhere else entirely (no upward walk needed).
func TestLoadTaskEnvFileFrom_ExplicitDir(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, workflowrunner.TaskEnvFileName), []byte("CS_CLOUD_TASK_ID=from-explicit\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	// cwd is an unrelated dir with no .cs-cloud.env.
	t.Chdir(t.TempDir())
	t.Setenv("CS_CLOUD_TASK_ID", "")

	loadTaskEnvFileFrom(root)

	if got := os.Getenv("CS_CLOUD_TASK_ID"); got != "from-explicit" {
		t.Errorf("CS_CLOUD_TASK_ID = %q, want from-explicit (loaded from the passed dir)", got)
	}
}

// TestWorkflowCmd_ConsumesTaskFlag verifies workflowCmd consumes `--task <id>`
// at the workflow level so it never reaches subcommand dispatch (where it would
// be treated as an unknown command). Uses a real app because the --task path
// reads config to drive the scan; the id matches no dir, so the scan whiffs,
// falls back to cwd walk-up, and dispatch reaches the task subcommand.
func TestWorkflowCmd_ConsumesTaskFlag(t *testing.T) {
	platform.SetDataDir(t.TempDir())
	t.Setenv("COSTRICT_BASE_URL", "https://example.costrict.local")
	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if err := workflowCmd(a, []string{"--task", "no-such-id", "task", "--help"}); err != nil {
		t.Fatalf("workflowCmd leaked --task into subcommand dispatch or failed: %v", err)
	}
}

func TestLoadTaskEnvFileIgnoresOversizedFile(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("CS_CLOUD_TASK_ID", "")
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
