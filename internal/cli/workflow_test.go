package cli

import (
	"testing"

	"cs-cloud/internal/app"
	"cs-cloud/internal/platform"
)

func TestWorkflowWorkspaceListEmptyCache(t *testing.T) {
	platform.SetDataDir(t.TempDir())
	a, err := app.New()
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	if err := workflowWorkspaceList(a); err != nil {
		t.Fatalf("workflowWorkspaceList: %v", err)
	}
}
