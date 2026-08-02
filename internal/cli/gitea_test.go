package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	hasChanges        *bool  // nil → assume changes present (keeps existing happy-path tests green)
	hasChangesDirs    []string
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
func (f *fakeGitOps) HasChanges(dir string) (bool, error) {
	f.hasChangesDirs = append(f.hasChangesDirs, dir)
	if f.hasChanges != nil {
		return *f.hasChanges, nil
	}
	return true, nil
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

// TestSubmitDeliverable_ProviderEnvRoutesToGithub verifies CS_CLOUD_CODE_PROVIDER=github
// routes to submitGithubPR even without --mr.
func TestSubmitDeliverable_ProviderEnvRoutesToGithub(t *testing.T) {
	var submittedURL string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/submit") {
			var body struct {
				PullRequestURL string `json:"pull_request_url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			submittedURL = body.PullRequestURL
			jsonResponse(w, 200, map[string]any{"id": "sub-1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer backend.Close()

	var gotAuthHeader string
	githubSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthHeader = r.Header.Get("Authorization")
		jsonResponse(w, 201, map[string]any{"html_url": "https://github.com/org/repo/pull/1", "number": 1})
	}))
	defer githubSrv.Close()

	t.Setenv("CS_CLOUD_CODE_PROVIDER", "github")
	t.Setenv("CS_CLOUD_GITHUB_TOKEN", "ghp-test-token")
	t.Setenv("CS_CLOUD_GITHUB_API_BASE", githubSrv.URL)
	t.Setenv("CS_CLOUD_BACKEND_URL", backend.URL)
	t.Setenv("CS_CLOUD_TOKEN", "tok")
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "nr-1")
	t.Setenv("CS_CLOUD_AGENT_ID", "agent-1")
	t.Setenv("CS_CLOUD_TASK_ID", "task-1")

	repoDir := t.TempDir()
	t.Chdir(repoDir)

	fake := &fakeGitOps{currentBranch: "feat/test"}
	stderr := captureStderr(t, func() {
		err := submitDeliverable(submitConfig{
			mrMode:        false, // no --mr — provider env drives routing
			deliverableID: "d1",
			repoURL:       "https://github.com/org/repo.git",
			gitOps:        fake,
		})
		if err != nil {
			t.Fatalf("submitDeliverable (github provider): %v", err)
		}
	})
	for _, want := range []string{
		"deliverable d1: submitting GitHub PR node_run=nr-1 branch=feat/test",
		"deliverable d1: pushing PR branch=feat/test",
		"deliverable d1: opening PR source=feat/test target=main",
		"deliverable d1: reporting PR",
		"deliverable d1: submitted pr=https://github.com/org/repo/pull/1",
	} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	if submittedURL != "https://github.com/org/repo/pull/1" {
		t.Errorf("submitted URL = %q, want github PR html_url", submittedURL)
	}
	if !strings.Contains(gotAuthHeader, "token ghp-test-token") {
		t.Errorf("Authorization header = %q, want 'token ghp-test-token'", gotAuthHeader)
	}
	// Assert push used the worktree dir (cwd).
	if len(fake.currentBranchDirs) != 1 || fake.currentBranchDirs[0] != repoDir {
		t.Errorf("CurrentBranch dir = %+v, want %q", fake.currentBranchDirs, repoDir)
	}
	if len(fake.pushCalls) != 1 || fake.pushCalls[0].branch != "feat/test" {
		t.Errorf("expected push of feat/test, got %+v", fake.pushCalls)
	}
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

func TestInjectTokenIntoURL_HostAwareUsername(t *testing.T) {
	tests := []struct {
		name         string
		cloneURL     string
		token        string
		wantContains string
	}{
		{"gitea uses oauth2", "http://gitea:3000/t-aaa/wf-bbb.git", "tok123", "oauth2:tok123@"},
		{"gitlab uses oauth2", "https://gitlab.test/g/r.git", "tok", "oauth2:tok@"},
		{"github.com uses x-access-token", "https://github.com/org/repo.git", "ghp", "x-access-token:ghp@"},
		{"github.com uppercase host", "https://GITHUB.COM/org/repo.git", "ghp", "x-access-token:ghp@"},
		{"GHE uses x-access-token", "https://ghe.example.com/org/repo.git", "ghp", "x-access-token:ghp@"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := injectTokenIntoURL(tt.cloneURL, tt.token)
			if got == "" {
				t.Fatalf("injectTokenIntoURL(%q, %q) returned empty", tt.cloneURL, tt.token)
			}
			if !strings.Contains(got, tt.wantContains) {
				t.Errorf("injectTokenIntoURL(%q, %q) = %q, want substring %q", tt.cloneURL, tt.token, got, tt.wantContains)
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

func TestNormalizeGiteaBase(t *testing.T) {
	cases := []struct {
		name              string
		base, owner, repo string
		want              string
	}{
		{"server root", "https://gitea.test", "t-aaa", "wf-bbb", "https://gitea.test"},
		{"repo web url stripped", "https://gitea.test/t-aaa/wf-bbb", "t-aaa", "wf-bbb", "https://gitea.test"},
		{"repo git url stripped", "https://gitea.test/t-aaa/wf-bbb.git", "t-aaa", "wf-bbb", "https://gitea.test"},
		{"repo url with port and trailing slash", "https://zgsmtest.xyz:30443/t-ad9d561c/wf-deliverable-archive/", "t-ad9d561c", "wf-deliverable-archive", "https://zgsmtest.xyz:30443"},
		{"unrelated repo suffix not stripped", "https://gitea.test/t-aaa/wf-bbb-extra", "t-aaa", "wf-bbb", "https://gitea.test/t-aaa/wf-bbb-extra"},
		{"server path prefix preserved", "https://corp.example/gitea/t-aaa/wf-bbb", "t-aaa", "wf-bbb", "https://corp.example/gitea"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := normalizeGiteaBase(c.base, c.owner, c.repo); got != c.want {
				t.Errorf("normalizeGiteaBase(%q,%q,%q) = %q, want %q", c.base, c.owner, c.repo, got, c.want)
			}
		})
	}
}

// TestOpenGiteaPR_NormalizesRepoPathBase reproduces the zgsmtest incident:
// CS_CLOUD_GITEA_BASE_URL carried the /<owner>/<repo> repo path, so the PR POST
// went to /<owner>/<repo>/api/v1/repos/<owner>/<repo>/pulls and Gitea 404'd.
// openGiteaPR must normalize the base to the server root before appending the
// API path.
func TestOpenGiteaPR_NormalizesRepoPathBase(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Method == http.MethodPost && gotPath == "/api/v1/repos/t-aaa/wf-bbb/pulls" {
			jsonResponse(w, 201, map[string]any{"number": 9, "html_url": "https://gitea.test/t-aaa/wf-bbb/pulls/9"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// base deliberately carries the repo path, as the misconfigured deploy did.
	prURL, err := openGiteaPR(context.Background(), srv.URL+"/t-aaa/wf-bbb", "tok", "t-aaa", "wf-bbb", "node/x", "inst-y", "d1")
	if err != nil {
		t.Fatalf("openGiteaPR: %v (gotPath=%q)", err, gotPath)
	}
	if gotPath != "/api/v1/repos/t-aaa/wf-bbb/pulls" {
		t.Fatalf("request path = %q, want /api/v1/repos/t-aaa/wf-bbb/pulls (base not normalized)", gotPath)
	}
	if prURL != "https://gitea.test/t-aaa/wf-bbb/pulls/9" {
		t.Errorf("prURL = %q, want html_url", prURL)
	}
}

// TestSubmitDeliverable_SkipsCommitWhenClean reproduces the re-run incident:
// the document was already committed (byte-identical), so `git commit` would
// exit 1 and abort the whole pipeline. submit must detect the clean tree,
// skip commit, and still push / open PR / report so the command is idempotent.
func TestSubmitDeliverable_SkipsCommitWhenClean(t *testing.T) {
	var reportedURL string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/node-runs/nr-1/deliverables/d1/submit" {
			var body struct {
				PullRequestURL string `json:"pull_request_url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reportedURL = body.PullRequestURL
			jsonResponse(w, 200, map[string]any{"id": "sub-1"})
			return
		}
		http.NotFound(w, r)
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

	t.Chdir(t.TempDir())
	t.Setenv("CS_CLOUD_TOKEN", "tok")
	t.Setenv("CS_CLOUD_BACKEND_URL", backend.URL)
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "nr-1")
	t.Setenv("CS_CLOUD_GITEA_BASE_URL", "https://gitea.test")
	t.Setenv("CS_CLOUD_GITEA_TOKEN", "pat-xyz")
	t.Setenv("CS_CLOUD_GITEA_OWNER", "t-aaa")
	t.Setenv("CS_CLOUD_GITEA_REPO", "wf-bbb")
	t.Setenv("CS_CLOUD_GITEA_CLONE_URL", "https://gitea.test/t-aaa/wf-bbb.git")
	t.Setenv("CS_CLOUD_GITEA_INST_BRANCH", "inst-cc")
	t.Setenv("CS_CLOUD_GITEA_NODE_BRANCH", "node/dd")
	t.Setenv("CS_CLOUD_GITEA_DELIVERABLES", `[{"deliverable_id":"d1","title":"Doc","path":"nodes/dd/d1.md"}]`)

	noChanges := false
	fake := &fakeGitOps{currentBranch: "node/dd", hasChanges: &noChanges}
	if err := submitDeliverable(submitConfig{
		giteaBaseOverride: giteaSrv.URL,
		deliverableID:     "d1",
		filePath:          tempFile(t, "body"),
		gitOps:            fake,
	}); err != nil {
		t.Fatalf("submitDeliverable: %v", err)
	}
	if len(fake.commitMsgs) != 0 {
		t.Errorf("expected NO commit when working tree is clean, got %+v", fake.commitMsgs)
	}
	if len(fake.pushCalls) != 1 {
		t.Errorf("expected push to still run, got %+v", fake.pushCalls)
	}
	if reportedURL == "" {
		t.Error("expected PR to be reported to backend despite clean tree")
	}
}

// TestOpenGiteaPR_AlreadyExistsReturnsExistingURL covers the re-run case where
// the PR was already opened (e.g. by a previous successful run). Gitea returns
// 409 on the POST; openGiteaPR must look up the existing open PR and return its
// html_url instead of erroring, so reporting still proceeds.
func TestOpenGiteaPR_AlreadyExistsReturnsExistingURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls"):
			jsonResponse(w, 409, map[string]any{"message": "pull request already exists for these targets"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pulls"):
			jsonResponse(w, 200, []map[string]any{{
				"number":   6,
				"html_url": "https://gitea.test/t-aaa/wf-bbb/pulls/6",
				"head":     map[string]any{"ref": "node/x"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prURL, err := openGiteaPR(context.Background(), srv.URL, "tok", "t-aaa", "wf-bbb", "node/x", "inst-y", "d1")
	if err != nil {
		t.Fatalf("openGiteaPR: %v", err)
	}
	if prURL != "https://gitea.test/t-aaa/wf-bbb/pulls/6" {
		t.Errorf("prURL = %q, want existing PR html_url", prURL)
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

// TestExecGitOps_CommitIsIdempotent drives the PRODUCTION execGitOps against a
// real git repository. The submit-flow tests above use fakeGitOps (which cannot
// reproduce `git commit` exiting 1 on a clean tree); this proves RC2's real-git
// behavior: an identical re-write leaves the tree clean so the caller skips
// commit, and a genuine change is detected + committed.
func TestExecGitOps_CommitIsIdempotent(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@test"},
		{"config", "user.name", "t"},
	} {
		c := exec.Command("git", append([]string{"-C", repo}, args...)...)
		if out, err := c.CombinedOutput(); err != nil {
			t.Skipf("git setup failed (%v): %s", err, out)
		}
	}
	var ops execGitOps
	write := func(content string) {
		t.Helper()
		if err := ops.WriteFile(repo, "nodes/d1.md", []byte(content)); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	dirty := func(want bool) {
		t.Helper()
		got, err := ops.HasChanges(repo)
		if err != nil {
			t.Fatalf("HasChanges: %v", err)
		}
		if got != want {
			t.Fatalf("HasChanges = %v, want %v", got, want)
		}
	}

	write("body") // new file → dirty
	dirty(true)
	if err := ops.Commit(repo, "deliverable: d1"); err != nil {
		t.Fatalf("Commit #1: %v", err)
	}
	dirty(false)

	write("body") // identical re-write → still clean (RC2: caller skips commit)
	dirty(false)

	write("body-v2") // real change → dirty again
	dirty(true)
	if err := ops.Commit(repo, "deliverable: d1"); err != nil {
		t.Fatalf("Commit #2: %v", err)
	}
	dirty(false)
}

// gitRun runs git -C dir and fails the test on error.
func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git -C %s %s: %v\n%s", dir, strings.Join(args, " "), err, out)
	}
}

// serveGitHTTP serves a local bare-repo "platform Gitea" via `git http-backend`
// so cs-cloud's real `git push` (over an http:// URL with the token embedded by
// injectTokenIntoURL) lands in a real repository.
func serveGitHTTP(t *testing.T, root string, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cmd := exec.Command("git", "http-backend")
	cmd.Env = []string{
		"GIT_PROJECT_ROOT=" + root,
		"GIT_HTTP_EXPORT_ALL=1",
		"PATH_INFO=" + r.URL.Path,
		"REQUEST_METHOD=" + r.Method,
		"QUERY_STRING=" + r.URL.RawQuery,
		"CONTENT_TYPE=" + r.Header.Get("Content-Type"),
		"CONTENT_LENGTH=" + strconv.Itoa(len(body)),
		"GATEWAY_INTERFACE=CGI/1.1",
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	cmd.Stdin = bytes.NewReader(body)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		http.Error(w, "git http-backend: "+err.Error(), http.StatusInternalServerError)
		return
	}
	rdr := bufio.NewReader(bytes.NewReader(out.Bytes()))
	status := http.StatusOK
	for {
		line, err := rdr.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Status:") {
			fmt.Sscanf(strings.TrimPrefix(line, "Status:"), "%d", &status)
		} else if i := strings.Index(line, ":"); i > 0 {
			w.Header().Set(line[:i], strings.TrimSpace(line[i+1:]))
		}
	}
	w.WriteHeader(status)
	_, _ = io.Copy(w, rdr)
}

// TestSubmitDeliverable_E2E_RealPush is a true end-to-end: a real git worktree
// pushed over HTTP (local git http-backend) into a bare "platform Gitea" repo,
// with the Gitea PR API and the multica backend report faked on the same HTTP
// server. Exercises the PRODUCTION execGitOps — real WriteFile / HasChanges /
// Commit / Push — under the http:// clone URL that carries cs-cloud's injected
// token, and asserts the doc actually lands on the bare repo's node branch and
// the PR URL is reported. Covers RC1 (base normalization) + RC2 (commit/push).
func TestSubmitDeliverable_E2E_RealPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	bare := filepath.Join(root, "t-aaa", "wf-bbb.git")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, bare, "init", "--bare", "-q")
	gitRun(t, bare, "config", "http.receivepack", "true")

	wt := t.TempDir()
	gitRun(t, wt, "init", "-q")
	gitRun(t, wt, "config", "user.email", "t@t")
	gitRun(t, wt, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(wt, "README.md"), []byte("init"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, wt, "add", "-A")
	gitRun(t, wt, "commit", "-qm", "init")
	gitRun(t, wt, "checkout", "-q", "-b", "node/dd")

	var reportedURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/t-aaa/wf-bbb.git/"):
			serveGitHTTP(t, root, w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/repos/t-aaa/wf-bbb/pulls":
			jsonResponse(w, 201, map[string]any{"number": 7, "html_url": "https://gitea.test/t-aaa/wf-bbb/pulls/7"})
		case r.URL.Path == "/api/node-runs/nr-1/deliverables/d1/submit":
			var body struct {
				PullRequestURL string `json:"pull_request_url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reportedURL = body.PullRequestURL
			jsonResponse(w, 200, map[string]any{"id": "sub-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("CS_CLOUD_NODE_RUN_ID", "nr-1")
	t.Setenv("CS_CLOUD_GITEA_OWNER", "t-aaa")
	t.Setenv("CS_CLOUD_GITEA_REPO", "wf-bbb")
	t.Setenv("CS_CLOUD_GITEA_INST_BRANCH", "inst-cc")
	t.Setenv("CS_CLOUD_GITEA_NODE_BRANCH", "node/dd")
	t.Setenv("CS_CLOUD_GITEA_DELIVERABLES", `[{"deliverable_id":"d1","title":"Doc","path":"nodes/dd/d1.md"}]`)
	t.Setenv("CS_CLOUD_GITEA_CLONE_URL", srv.URL+"/t-aaa/wf-bbb.git")
	t.Setenv("CS_CLOUD_GITEA_BASE_URL", srv.URL)
	t.Setenv("CS_CLOUD_GITEA_TOKEN", "tok")
	t.Setenv("CS_CLOUD_BACKEND_URL", srv.URL)
	t.Setenv("CS_CLOUD_TOKEN", "tok")

	doc := filepath.Join(wt, "doc.md")
	if err := os.WriteFile(doc, []byte("# real deliverable body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(wt)

	if err := submitDeliverable(submitConfig{
		deliverableID: "d1",
		filePath:      doc,
		gitOps:        &execGitOps{},
	}); err != nil {
		t.Fatalf("submitDeliverable E2E: %v", err)
	}

	if reportedURL != "https://gitea.test/t-aaa/wf-bbb/pulls/7" {
		t.Errorf("reported PR = %q, want the html_url", reportedURL)
	}
	// Real proof the push happened: the bare "platform Gitea" now has node/dd
	// carrying the deliverable document.
	got, err := exec.Command("git", "-C", bare, "show", "node/dd:nodes/dd/d1.md").Output()
	if err != nil {
		t.Fatalf("bare repo has no node/dd deliverable: %v", err)
	}
	if string(got) != "# real deliverable body\n" {
		t.Errorf("bare repo node/dd:nodes/dd/d1.md = %q, want deliverable body", string(got))
	}
}
