package workflowrunner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cs-cloud/internal/workflow"
)

func gitCmd(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitCmdErr(t, dir, args...)
	if err != nil {
		t.Fatalf("git %v in %s: %v", args, dir, err)
	}
	return out
}

// gitCmdErr runs git and returns its stdout and error without failing the
// test, for assertions where a non-zero exit is the expected outcome.
func gitCmdErr(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("%s: %s", err, bytes.TrimSpace(stderr.Bytes()))
	}
	return string(out), nil
}

func gitCommit(t *testing.T, dir, path, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gitCmd(t, dir, "add", "--", path)
	gitCmd(t, dir, "commit", "-m", "add "+path)
}

// TestPrepareDeliveryRepo verifies cs-cloud deterministically clones the
// delivery repo, pulls inst first (carrying upstream deliverables), then checks
// out the node branch — so the agent starts on node with the latest upstream
// readable via inst. Origin layout: main(base) → inst(base+upstream),
// node(cut from inst BEFORE upstream landed, so node lacks it).
func TestPrepareDeliveryRepo(t *testing.T) {
	origin := t.TempDir()
	gitCmd(t, origin, "init")
	gitCmd(t, origin, "config", "user.email", "t@t")
	gitCmd(t, origin, "config", "user.name", "T")
	gitCommit(t, origin, "README.md", "base\n")
	gitCmd(t, origin, "branch", "-m", "main")
	gitCmd(t, origin, "branch", "inst")         // inst from main (base)
	gitCmd(t, origin, "branch", "node", "inst") // node from inst (base — before upstream)
	gitCmd(t, origin, "checkout", "inst")
	gitCommit(t, origin, "upstream.md", "upstream deliverable\n") // advance inst
	gitCmd(t, origin, "checkout", "main")

	worktree := t.TempDir()
	payload := workflow.TaskRunPayload{
		Repos: []workflow.RepoSpec{{Role: "delivery", URL: origin, Alias: "delivery"}},
	}
	env := []string{
		"CS_CLOUD_GITEA_TOKEN=ignored-for-file-url",
		"CS_CLOUD_GITEA_INST_BRANCH=inst",
		"CS_CLOUD_GITEA_NODE_BRANCH=node",
	}
	if err := prepareDeliveryRepo(context.Background(), worktree, payload, env); err != nil {
		t.Fatalf("prepareDeliveryRepo: %v", err)
	}

	cloneDir := filepath.Join(worktree, "delivery")
	if !dirExists(cloneDir) {
		t.Fatalf("delivery clone not created at %s", cloneDir)
	}
	if got := strings.TrimSpace(gitCmd(t, cloneDir, "rev-parse", "--abbrev-ref", "HEAD")); got != "node" {
		t.Fatalf("HEAD = %q, want node", got)
	}
	// inst is fresh and carries the upstream deliverable (agent reads on demand).
	if got := strings.TrimSpace(gitCmd(t, cloneDir, "show", "inst:upstream.md")); got != "upstream deliverable" {
		t.Fatalf("inst:upstream.md = %q, want upstream deliverable", got)
	}
}

// TestPrepareDeliveryRepo_ReworkReusesClone verifies a second call (rework
// reusing the workdir) does not re-clone but fetches + ends on node.
func TestPrepareDeliveryRepo_ReworkReusesClone(t *testing.T) {
	origin := t.TempDir()
	gitCmd(t, origin, "init")
	gitCmd(t, origin, "config", "user.email", "t@t")
	gitCmd(t, origin, "config", "user.name", "T")
	gitCommit(t, origin, "README.md", "base\n")
	gitCmd(t, origin, "branch", "-m", "main")
	gitCmd(t, origin, "branch", "inst")
	gitCmd(t, origin, "branch", "node", "inst")

	worktree := t.TempDir()
	payload := workflow.TaskRunPayload{
		Repos: []workflow.RepoSpec{{Role: "delivery", URL: origin, Alias: "delivery"}},
	}
	env := []string{"CS_CLOUD_GITEA_INST_BRANCH=inst", "CS_CLOUD_GITEA_NODE_BRANCH=node"}

	if err := prepareDeliveryRepo(context.Background(), worktree, payload, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Advance origin's node after the first clone, then call again (rework).
	gitCmd(t, origin, "checkout", "node")
	gitCommit(t, origin, "v2.md", "round 2\n")
	gitCmd(t, origin, "checkout", "main")

	if err := prepareDeliveryRepo(context.Background(), worktree, payload, env); err != nil {
		t.Fatalf("rework call: %v", err)
	}
	cloneDir := filepath.Join(worktree, "delivery")
	if got := strings.TrimSpace(gitCmd(t, cloneDir, "rev-parse", "--abbrev-ref", "HEAD")); got != "node" {
		t.Fatalf("HEAD = %q, want node", got)
	}
	if got := strings.TrimSpace(gitCmd(t, cloneDir, "show", "node:v2.md")); got != "round 2" {
		t.Fatalf("node:v2.md = %q, want round 2 (rework should have fetched latest)", got)
	}
}

// TestPrepareDeliveryRepo_NoDeliveryRepo is a no-op when there is no delivery repo.
func TestPrepareDeliveryRepo_NoDeliveryRepo(t *testing.T) {
	if err := prepareDeliveryRepo(context.Background(), t.TempDir(), workflow.TaskRunPayload{}, nil); err != nil {
		t.Fatalf("expected nil for no delivery repo, got %v", err)
	}
}

// TestPrepareDeliveryRepo_ForcePushDivergence verifies `checkout -B` force-syncs
// the local node to the server's node even when the worker force-pushed (rewrote
// node history) so the local and remote diverged — a plain `pull --ff-only`
// would fail non-ff here. This is the rework scenario the -B sequence exists for.
//
// To exercise a real force-push (not just a fast-forward append), origin's node
// starts with a "round 0" commit that the first clone observes; the worker then
// resets node back to base and commits a different file, so origin/node's history
// is rewritten (the round-0 commit is no longer an ancestor) — exactly what the
// worker does each rework round.
func TestPrepareDeliveryRepo_ForcePushDivergence(t *testing.T) {
	origin := t.TempDir()
	gitCmd(t, origin, "init")
	gitCmd(t, origin, "config", "user.email", "t@t")
	gitCmd(t, origin, "config", "user.name", "T")
	gitCommit(t, origin, "README.md", "base\n")
	gitCmd(t, origin, "branch", "-m", "main")
	gitCmd(t, origin, "branch", "inst")
	gitCmd(t, origin, "branch", "node", "inst")
	// Round 0: origin's node carries seed.md, which the first clone observes.
	gitCmd(t, origin, "checkout", "node")
	gitCommit(t, origin, "seed.md", "seed\n")
	gitCmd(t, origin, "checkout", "main")

	worktree := t.TempDir()
	payload := workflow.TaskRunPayload{
		Repos: []workflow.RepoSpec{{Role: "delivery", URL: origin, Alias: "delivery"}},
	}
	env := []string{"CS_CLOUD_GITEA_INST_BRANCH=inst", "CS_CLOUD_GITEA_NODE_BRANCH=node"}

	if err := prepareDeliveryRepo(context.Background(), worktree, payload, env); err != nil {
		t.Fatalf("first call: %v", err)
	}
	cloneDir := filepath.Join(worktree, "delivery")
	gitCmd(t, cloneDir, "config", "user.email", "t@t")
	gitCmd(t, cloneDir, "config", "user.name", "T")

	// Worker round 1: local node advances on top of seed.
	gitCommit(t, cloneDir, "worker.md", "round 1\n")
	// Meanwhile origin REWRITES node: reset it back to base (dropping seed) and
	// commit a different file — a non-fast-forward force-push, not an append.
	gitCmd(t, origin, "checkout", "node")
	gitCmd(t, origin, "reset", "--hard", "main")
	gitCommit(t, origin, "origin.md", "origin rewrite\n")
	gitCmd(t, origin, "checkout", "main")

	// Rework: -B must force the local node onto origin's rewritten node.
	if err := prepareDeliveryRepo(context.Background(), worktree, payload, env); err != nil {
		t.Fatalf("rework call: %v", err)
	}
	if got := strings.TrimSpace(gitCmd(t, cloneDir, "rev-parse", "--abbrev-ref", "HEAD")); got != "node" {
		t.Fatalf("HEAD = %q, want node", got)
	}
	headSHA := strings.TrimSpace(gitCmd(t, cloneDir, "rev-parse", "HEAD"))
	originNodeSHA := strings.TrimSpace(gitCmd(t, cloneDir, "rev-parse", "origin/node"))
	if headSHA != originNodeSHA {
		t.Fatalf("HEAD %s != origin/node %s (-B should have force-synced local node to server)", headSHA, originNodeSHA)
	}
	if got := strings.TrimSpace(gitCmd(t, cloneDir, "show", "node:origin.md")); got != "origin rewrite" {
		t.Fatalf("node:origin.md = %q, want origin rewrite", got)
	}
	// The rewritten node no longer carries the round-0 seed the worker discarded.
	if _, err := gitCmdErr(t, cloneDir, "show", "node:seed.md"); err == nil {
		t.Fatalf("node:seed.md should be absent after the force-push rewrite")
	}
}

// TestPrepareDeliveryRepo_RejectsTraversalAlias verifies a delivery repo alias
// that would escape the worktree (e.g. "../escape") is rejected before any git
// operation runs, so a malformed alias cannot clone outside the task workdir.
func TestPrepareDeliveryRepo_RejectsTraversalAlias(t *testing.T) {
	worktree := t.TempDir()
	payload := workflow.TaskRunPayload{
		Repos: []workflow.RepoSpec{{Role: "delivery", URL: "file:///unused", Alias: "../escape"}},
	}
	env := []string{"CS_CLOUD_GITEA_INST_BRANCH=inst", "CS_CLOUD_GITEA_NODE_BRANCH=node"}
	if err := prepareDeliveryRepo(context.Background(), worktree, payload, env); err == nil {
		t.Fatal("expected error for path-traversal alias, got nil")
	}
	escapeDir := filepath.Join(worktree, "..", "escape")
	if dirExists(escapeDir) {
		t.Fatalf("traversal alias escaped worktree: %s was created", escapeDir)
	}
}
