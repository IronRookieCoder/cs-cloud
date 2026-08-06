package cli

import (
	"strings"
	"testing"
)

// TestGiteaContext_DeliverablePath_RejectsUnknownID covers the deliverable-id
// validation branch (R5): an id not present in CS_CLOUD_GITEA_DELIVERABLES must
// be rejected before any write/push/PR, so an agent can't submit under an id
// the server didn't register. This branch had no test coverage before.
func TestGiteaContext_DeliverablePath_RejectsUnknownID(t *testing.T) {
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "nr-1")
	t.Setenv("CS_CLOUD_GITEA_OWNER", "o")
	t.Setenv("CS_CLOUD_GITEA_REPO", "r")
	t.Setenv("CS_CLOUD_GITEA_CLONE_URL", "https://gitea.test/o/r.git")
	t.Setenv("CS_CLOUD_GITEA_INST_BRANCH", "inst-1")
	t.Setenv("CS_CLOUD_GITEA_NODE_BRANCH", "node-1")
	t.Setenv("CS_CLOUD_GITEA_DELIVERABLES", `[{"deliverable_id":"del-1","title":"t","path":"nodes/x.md"}]`)

	gctx, err := readGiteaContext()
	if err != nil {
		t.Fatalf("readGiteaContext: %v", err)
	}

	_, err = gctx.deliverablePath("del-unknown")
	if err == nil {
		t.Fatal("expected error rejecting an unknown deliverable id")
	}
	if !strings.Contains(err.Error(), "not in CS_CLOUD_GITEA_DELIVERABLES") {
		t.Fatalf("error = %v, want it to mention CS_CLOUD_GITEA_DELIVERABLES", err)
	}

	// Known id resolves to its registered path.
	path, err := gctx.deliverablePath("del-1")
	if err != nil {
		t.Fatalf("known id: %v", err)
	}
	if path != "nodes/x.md" {
		t.Fatalf("path = %q, want nodes/x.md", path)
	}
}
