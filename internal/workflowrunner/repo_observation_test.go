package workflowrunner

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cs-cloud/internal/workflow"
)

func TestRepoDownloadObservationsMatchExpectedRemotes(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	codeDir := filepath.Join(root, "code")
	runGit(t, root, "init", codeDir)
	runGit(t, codeDir, "remote", "add", "origin", "https://oauth2:secret@gitlab.test/group/code.git")

	repos := []workflow.RepoSpec{
		{Role: "code", Provider: "gitlab", Alias: "code", URL: "https://gitlab.test/group/code.git"},
		{Role: "delivery", Provider: "gitea", Alias: "docs", URL: "https://gitea.test/team/deliverables.git"},
	}

	observations := observeRepoDownloads(root, repos)
	if len(observations) != 2 {
		t.Fatalf("observations = %+v, want 2 entries", observations)
	}
	if !observations[0].found || observations[0].path != codeDir {
		t.Fatalf("code repo observation = %+v, want found at %s", observations[0], codeDir)
	}
	if observations[1].found {
		t.Fatalf("delivery repo observation = %+v, want missing", observations[1])
	}

	summary := repoDownloadObservationSummary(observations)
	if !strings.Contains(summary, "code:gitlab:code=found") || !strings.Contains(summary, "delivery:gitea:docs=missing") {
		t.Fatalf("summary = %q", summary)
	}
	if strings.Contains(summary, "secret") {
		t.Fatalf("summary leaked credential: %q", summary)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
