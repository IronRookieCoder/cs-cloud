package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeGitOps records the sequence of git operations without touching the
// filesystem or a real git binary. Each method records the dir it was called
// with so tests can assert operations target the correct worktree (not the
// bare task root).
type fakeGitOps struct {
	cloneCalls []struct{ authURL, branch, dir string }
	written    []struct {
		dir     string
		path    string
		content []byte
	}
	commitMsgs        []string
	pushCalls         []struct{ dir, authURL, branch string }
	currentBranchDirs []string
	currentBranch     string // value returned by CurrentBranch
}

func (f *fakeGitOps) Clone(authURL, branch, dir string) error {
	f.cloneCalls = append(f.cloneCalls, struct{ authURL, branch, dir string }{authURL, branch, dir})
	return nil
}
func (f *fakeGitOps) WriteFile(dir, path string, content []byte) error {
	f.written = append(f.written, struct {
		dir     string
		path    string
		content []byte
	}{dir, path, content})
	return nil
}
func (f *fakeGitOps) Commit(dir, message string) error {
	f.commitMsgs = append(f.commitMsgs, message)
	return nil
}
func (f *fakeGitOps) Push(dir, authURL, branch string) error {
	f.pushCalls = append(f.pushCalls, struct{ dir, authURL, branch string }{dir, authURL, branch})
	return nil
}
func (f *fakeGitOps) CurrentBranch(dir string) (string, error) {
	f.currentBranchDirs = append(f.currentBranchDirs, dir)
	return f.currentBranch, nil
}

// TestSubmitDeliverable_HappyPath wires a fake git + httptest Gitea + httptest
// the server and asserts the worktree-based document submit flow: the agent has
// already run `cs-cloud repo checkout` (creating a delivery worktree under the
// task root), so submit reads CS_CLOUD_WORKTREE (the task root), resolves the
// per-repo worktree subdir, reads the current branch from that worktree, writes
// the --file content into the deliverable path, commits, pushes the worktree's
// branch, opens a Gitea PR (head=worktree branch, base=inst), and reports the
// PR URL. NO clone / MkdirTemp / PrepareBranch — those are tmp-clone leftovers.
func TestSubmitDeliverable_HappyPath(t *testing.T) {
	var reportedURL string
	var gotAgentID, gotTaskID string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/gitea/credential":
			jsonResponse(w, 200, map[string]string{"base_url": "https://gitea.test", "token": "pat-xyz"})
		case "/api/node-runs/nr-1/deliverables/d1/submit":
			var body struct {
				PullRequestURL string `json:"pull_request_url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reportedURL = body.PullRequestURL
			gotAgentID = r.Header.Get("X-Agent-ID")
			gotTaskID = r.Header.Get("X-Task-ID")
			jsonResponse(w, 200, map[string]any{"id": "sub-1", "pull_request_url": body.PullRequestURL})
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	giteaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") {
			jsonResponse(w, 201, map[string]any{"number": 7, "html_url": "https://gitea.test/t-aaa/wf-bbb/pulls/7"})
			return
		}
		http.NotFound(w, r)
	}))
	defer giteaSrv.Close()

	// The agent cloned the delivery repo and runs this command from inside it,
	// so submit resolves the repo dir from the current working directory.
	repoDir := t.TempDir()
	t.Chdir(repoDir)
	t.Setenv("CS_CLOUD_TOKEN", "tok")
	t.Setenv("CS_CLOUD_BACKEND_URL", backend.URL)
	t.Setenv("CS_CLOUD_WORKSPACE_ID", "ws-1")
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "nr-1")
	t.Setenv("CS_CLOUD_GITEA_BASE_URL", "https://gitea.test")
	t.Setenv("CS_CLOUD_GITEA_TOKEN", "pat-xyz")
	t.Setenv("CS_CLOUD_GITEA_OWNER", "t-aaa")
	t.Setenv("CS_CLOUD_GITEA_REPO", "wf-bbb")
	// CS_CLOUD_GITEA_CLONE_URL is what the document path feeds into
	// RepoWorktreeDir to resolve the worktree subdir. Must match the
	// <owner>/<repo>.git shape backend emits so repoName(cloneURL) = "wf-bbb".
	t.Setenv("CS_CLOUD_GITEA_CLONE_URL", "https://gitea.test/t-aaa/wf-bbb.git")
	t.Setenv("CS_CLOUD_GITEA_INST_BRANCH", "inst-cc")
	t.Setenv("CS_CLOUD_GITEA_NODE_BRANCH", "node/dd")
	t.Setenv("CS_CLOUD_GITEA_DELIVERABLES", `[{"deliverable_id":"d1","title":"Doc","path":"nodes/dd/d1.md"}]`)
	t.Setenv("CS_CLOUD_AGENT_ID", "agent-uuid-111")
	t.Setenv("CS_CLOUD_TASK_ID", "task-uuid-222")

	tmpFile := tempFile(t, "# my document body")

	// currentBranch mirrors what CheckoutRepo would have left the worktree on:
	// the env-advertised CS_CLOUD_REPO_NODE_BRANCH (here aliased as
	// CS_CLOUD_GITEA_NODE_BRANCH = "node/dd").
	fake := &fakeGitOps{currentBranch: "node/dd"}
	stderr := captureStderr(t, func() {
		err := submitDeliverable(submitConfig{
			giteaBaseOverride: giteaSrv.URL,
			deliverableID:     "d1",
			filePath:          tmpFile,
			gitOps:            fake,
		})
		if err != nil {
			t.Fatalf("submitDeliverable: %v", err)
		}
	})
	for _, want := range []string{
		"deliverable d1: submitting node_run=nr-1 branch=node/dd",
		"deliverable d1: pushing branch=node/dd",
		"deliverable d1: opening PR head=node/dd base=inst-cc",
		"deliverable d1: reporting PR",
		"deliverable d1: submitted pr=https://gitea.test/t-aaa/wf-bbb/pulls/7",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}

	// NO tmp-clone leftovers: the worktree already exists (agent ran checkout).
	// (cloneCalls == 0 proves no tmp-clone; PrepareBranch was removed entirely.)
	if len(fake.cloneCalls) != 0 {
		t.Errorf("expected NO clone (worktree-based), got %+v", fake.cloneCalls)
	}

	// WriteFile + Commit + Push + CurrentBranch all operate on the repo the
	// agent is running inside (cwd), which submit resolves via os.Getwd().
	wantWorktree := repoDir
	if len(fake.currentBranchDirs) != 1 || fake.currentBranchDirs[0] != wantWorktree {
		t.Errorf("CurrentBranch dir = %+v, want %q (the delivery worktree subdir)",
			fake.currentBranchDirs, wantWorktree)
	}
	if len(fake.written) != 1 || fake.written[0].dir != wantWorktree {
		t.Errorf("expected write to worktree %q, got %+v", wantWorktree, fake.written)
	}
	if len(fake.written) != 1 || fake.written[0].path != "nodes/dd/d1.md" {
		t.Errorf("expected file written to nodes/dd/d1.md, got %+v", fake.written)
	}
	if len(fake.commitMsgs) != 1 {
		t.Errorf("expected one commit, got %+v", fake.commitMsgs)
	}
	if len(fake.pushCalls) != 1 || fake.pushCalls[0].dir != wantWorktree {
		t.Errorf("Push dir = %+v, want %q (the delivery worktree subdir)",
			fake.pushCalls, wantWorktree)
	}
	if len(fake.pushCalls) != 1 || fake.pushCalls[0].branch != "node/dd" {
		t.Errorf("expected push of worktree branch node/dd, got %+v", fake.pushCalls)
	}
	if reportedURL != "https://gitea.test/t-aaa/wf-bbb/pulls/7" {
		t.Errorf("submit received %q, want the PR html_url", reportedURL)
	}
	if gotAgentID != "agent-uuid-111" {
		t.Errorf("X-Agent-ID = %q, want agent-uuid-111", gotAgentID)
	}
	if gotTaskID != "task-uuid-222" {
		t.Errorf("X-Task-ID = %q, want task-uuid-222", gotTaskID)
	}
}

func TestSubmitDeliverable_GitLabMR(t *testing.T) {
	// Fake GitLab: POST /api/v4/projects/<enc>/merge_requests
	var gitlabReqBody map[string]any
	gitlabSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/merge_requests") {
			if got := r.Header.Get("PRIVATE-TOKEN"); got != "gl-pat" {
				t.Errorf("PRIVATE-TOKEN = %q, want gl-pat", got)
			}
			_ = json.NewDecoder(r.Body).Decode(&gitlabReqBody)
			jsonResponse(w, 201, map[string]any{"web_url": "https://gitlab.test/group/repo/-/merge_requests/42"})
			return
		}
		http.NotFound(w, r)
	}))
	defer gitlabSrv.Close()

	// Fake backend: POST /api/node-runs/<nr>/deliverables/<did>/submit
	var submittedURL string
	var glAgentID, glTaskID string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PullRequestURL string `json:"pull_request_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		submittedURL = body.PullRequestURL
		glAgentID = r.Header.Get("X-Agent-ID")
		glTaskID = r.Header.Get("X-Task-ID")
		jsonResponse(w, 200, map[string]any{"id": "sub-1"})
	}))
	defer backend.Close()

	t.Setenv("CS_CLOUD_GITLAB_TOKEN", "gl-pat")
	t.Setenv("CS_CLOUD_GITLAB_BASE_URL", gitlabSrv.URL)
	t.Setenv("CS_CLOUD_BACKEND_URL", backend.URL)
	t.Setenv("CS_CLOUD_TOKEN", "tok")
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "nr-1")
	t.Setenv("CS_CLOUD_AGENT_ID", "agent-uuid-333")
	t.Setenv("CS_CLOUD_TASK_ID", "task-uuid-444")
	// The agent cloned the code repo and runs this command from inside it, so
	// submit resolves the repo dir from the current working directory.
	repoDir := t.TempDir()
	t.Chdir(repoDir)

	fake := &fakeGitOps{
		currentBranch: "feat/code-changes",
	}
	stderr := captureStderr(t, func() {
		err := submitDeliverable(submitConfig{
			mrMode:        true,
			deliverableID: "d1",
			repoURL:       "https://gitlab.test/group/mycode.git",
			gitOps:        fake,
		})
		if err != nil {
			t.Fatalf("submitDeliverable (mr): %v", err)
		}
	})
	for _, want := range []string{
		"deliverable d1: submitting MR node_run=nr-1 branch=feat/code-changes",
		"deliverable d1: pushing MR branch=feat/code-changes",
		"deliverable d1: opening MR source=feat/code-changes target=main",
		"deliverable d1: reporting MR",
		"deliverable d1: submitted mr=https://gitlab.test/group/repo/-/merge_requests/42",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}

	// submit pushes from the repo the agent is running inside (cwd).
	wantWorktree := repoDir
	if len(fake.currentBranchDirs) != 1 || fake.currentBranchDirs[0] != wantWorktree {
		t.Errorf("CurrentBranch dir = %+v, want %q (the code-repo worktree subdir)",
			fake.currentBranchDirs, wantWorktree)
	}
	if len(fake.pushCalls) != 1 || fake.pushCalls[0].dir != wantWorktree {
		t.Errorf("Push dir = %+v, want %q (the code-repo worktree subdir)",
			fake.pushCalls, wantWorktree)
	}
	if len(fake.pushCalls) != 1 || fake.pushCalls[0].branch != "feat/code-changes" {
		t.Errorf("expected push of feat/code-changes, got %+v", fake.pushCalls)
	}

	// Assert GitLab MR request
	if gitlabReqBody == nil {
		t.Fatal("GitLab merge_requests endpoint was not called")
	}
	if gitlabReqBody["source_branch"] != "feat/code-changes" {
		t.Errorf("source_branch = %v, want feat/code-changes", gitlabReqBody["source_branch"])
	}
	if gitlabReqBody["target_branch"] != "main" {
		t.Errorf("target_branch = %v, want main", gitlabReqBody["target_branch"])
	}

	// Assert backend submit received the MR URL
	if submittedURL != "https://gitlab.test/group/repo/-/merge_requests/42" {
		t.Errorf("submit received %q, want GitLab MR web_url", submittedURL)
	}
	if glAgentID != "agent-uuid-333" {
		t.Errorf("X-Agent-ID = %q, want agent-uuid-333", glAgentID)
	}
	if glTaskID != "task-uuid-444" {
		t.Errorf("X-Task-ID = %q, want task-uuid-444", glTaskID)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stderr: %v", err)
	}
	os.Stderr = w
	defer func() {
		os.Stderr = old
		_ = r.Close()
	}()
	fn()
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return string(out)
}

func TestSubmitDeliverable_MissingNodeRunID(t *testing.T) {
	// The failure must come from readGiteaContext detecting the missing
	// CS_CLOUD_NODE_RUN_ID (cwd is always valid, so no worktree-dir check).
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "")
	if err := submitDeliverable(submitConfig{deliverableID: "d1", filePath: "x", gitOps: &fakeGitOps{}}); err == nil {
		t.Fatal("expected error when CS_CLOUD_NODE_RUN_ID missing")
	}
}

func TestParseSubmitArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantDeliv   string
		wantFile    string
		wantMR      bool
		wantRepo    string
		wantErr     bool
		errContains string
	}{
		{"both flags", []string{"--deliverable", "d1", "--file", "/p/f.md"}, "d1", "/p/f.md", false, "", false, ""},
		{"flags reversed", []string{"--file", "/p/f.md", "--deliverable", "d1"}, "d1", "/p/f.md", false, "", false, ""},
		{"missing deliverable", []string{"--file", "/p/f.md"}, "", "", false, "", true, "--deliverable"},
		{"missing file", []string{"--deliverable", "d1"}, "", "", false, "", true, "--file"},
		{"deliverable no value", []string{"--deliverable"}, "", "", false, "", true, "--deliverable needs a value"},
		{"unknown arg", []string{"--deliverable", "d1", "--bogus"}, "", "", false, "", true, "unknown argument"},
		{"mr mode", []string{"--deliverable", "d1", "--mr", "--repo", "https://gl.test/g/r.git"}, "d1", "", true, "https://gl.test/g/r.git", false, ""},
		{"mr without repo", []string{"--deliverable", "d1", "--mr"}, "", "", false, "", true, "--repo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, f, mr, repo, err := parseSubmitArgs(tt.args)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("err = %q, want substring %q", err.Error(), tt.errContains)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d != tt.wantDeliv || f != tt.wantFile || mr != tt.wantMR || repo != tt.wantRepo {
				t.Errorf("got (%q,%q,%v,%q), want (%q,%q,%v,%q)", d, f, mr, repo, tt.wantDeliv, tt.wantFile, tt.wantMR, tt.wantRepo)
			}
		})
	}
}

func TestInjectTokenIntoURL(t *testing.T) {
	got := injectTokenIntoURL("http://gitea:3000/t-aaa/wf-bbb.git", "tok123")
	if got != "http://oauth2:tok123@gitea:3000/t-aaa/wf-bbb.git" {
		t.Errorf("injectTokenIntoURL = %q", got)
	}
	if injectTokenIntoURL("://bad", "tok") != "" {
		t.Error("expected empty for unparseable URL")
	}
}

func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func tempFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "doc-*.md")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(content)
	_ = f.Close()
	return f.Name()
}
