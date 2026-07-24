package cli

import (
	"io"
	"os"
	"strings"
	"testing"

	"cs-cloud/internal/app"
	"cs-cloud/internal/platform"
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
	if strings.Contains(out, "task:") {
		t.Fatalf("workflow usage should not advertise unimplemented task resource:\n%s", out)
	}
}
