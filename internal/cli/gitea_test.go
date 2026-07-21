package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitOps records the sequence of git operations without touching the
// filesystem or a real git binary.
type fakeGitOps struct {
	cloneCalls  []struct{ authURL, branch, dir string }
	branchCalls []string
	written     []struct {
		dir     string
		path    string
		content []byte
	}
	commitMsgs []string
	pushCalls  []string
}

func (f *fakeGitOps) Clone(authURL, branch, dir string) error {
	f.cloneCalls = append(f.cloneCalls, struct{ authURL, branch, dir string }{authURL, branch, dir})
	return nil
}
func (f *fakeGitOps) PrepareBranch(dir, nodeBranch string) error {
	f.branchCalls = append(f.branchCalls, nodeBranch)
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
	f.pushCalls = append(f.pushCalls, branch)
	return nil
}

// TestSubmitDeliverable_HappyPath wires a fake git + httptest Gitea + httptest
// Multica and asserts the full submit flow: credential fetch -> clone inst ->
// prepare node branch -> write file -> commit -> push -> open PR -> report-pr
// with the PR URL.
func TestSubmitDeliverable_HappyPath(t *testing.T) {
	var reportedURL string
	multica := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/gitea/credential":
			jsonResponse(w, 200, map[string]string{"base_url": "https://gitea.test", "token": "pat-xyz"})
		case "/api/daemon/node-runs/nr-1/deliverables/d1/report-pr":
			var body struct {
				PullRequestURL string `json:"pull_request_url"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			reportedURL = body.PullRequestURL
			jsonResponse(w, 200, map[string]any{"id": "sub-1", "pull_request_url": body.PullRequestURL})
		default:
			http.NotFound(w, r)
		}
	}))
	defer multica.Close()

	giteaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pulls") {
			jsonResponse(w, 201, map[string]any{"number": 7, "html_url": "https://gitea.test/t-aaa/wf-bbb/pulls/7"})
			return
		}
		http.NotFound(w, r)
	}))
	defer giteaSrv.Close()

	t.Setenv("MULTICA_TOKEN", "tok")
	t.Setenv("MULTICA_SERVER_URL", multica.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
	t.Setenv("MULTICA_NODE_RUN_ID", "nr-1")
	t.Setenv("MULTICA_GITEA_OWNER", "t-aaa")
	t.Setenv("MULTICA_GITEA_REPO", "wf-bbb")
	t.Setenv("MULTICA_GITEA_INST_BRANCH", "inst-cc")
	t.Setenv("MULTICA_GITEA_NODE_BRANCH", "node/dd")
	t.Setenv("MULTICA_GITEA_DELIVERABLES", `[{"deliverable_id":"d1","title":"Doc","path":"nodes/dd/d1.md"}]`)

	tmpFile := tempFile(t, "# my document body")

	fake := &fakeGitOps{}
	err := submitDeliverable(submitConfig{
		giteaBaseOverride: giteaSrv.URL,
		deliverableID:     "d1",
		filePath:          tmpFile,
		gitOps:            fake,
	})
	if err != nil {
		t.Fatalf("submitDeliverable: %v", err)
	}
	if len(fake.cloneCalls) != 1 || fake.cloneCalls[0].branch != "inst-cc" {
		t.Errorf("expected one clone of inst-cc, got %+v", fake.cloneCalls)
	}
	if len(fake.written) != 1 || fake.written[0].path != "nodes/dd/d1.md" {
		t.Errorf("expected file written to nodes/dd/d1.md, got %+v", fake.written)
	}
	if len(fake.pushCalls) != 1 || fake.pushCalls[0] != "node/dd" {
		t.Errorf("expected push of node/dd, got %+v", fake.pushCalls)
	}
	if reportedURL != "https://gitea.test/t-aaa/wf-bbb/pulls/7" {
		t.Errorf("report-pr received %q, want the PR html_url", reportedURL)
	}
}

func TestSubmitDeliverable_MissingNodeRunID(t *testing.T) {
	t.Setenv("MULTICA_NODE_RUN_ID", "")
	if err := submitDeliverable(submitConfig{deliverableID: "d1", filePath: "x", gitOps: &fakeGitOps{}}); err == nil {
		t.Fatal("expected error when MULTICA_NODE_RUN_ID missing")
	}
}

func TestParseSubmitArgs(t *testing.T) {
	tests := []struct {
		name        string
		args        []string
		wantDeliv   string
		wantFile    string
		wantErr     bool
		errContains string
	}{
		{"both flags", []string{"--deliverable", "d1", "--file", "/p/f.md"}, "d1", "/p/f.md", false, ""},
		{"flags reversed", []string{"--file", "/p/f.md", "--deliverable", "d1"}, "d1", "/p/f.md", false, ""},
		{"missing deliverable", []string{"--file", "/p/f.md"}, "", "", true, "--deliverable"},
		{"missing file", []string{"--deliverable", "d1"}, "", "", true, "--file"},
		{"deliverable no value", []string{"--deliverable"}, "", "", true, "--deliverable needs a value"},
		{"unknown arg", []string{"--deliverable", "d1", "--bogus"}, "", "", true, "unknown argument"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, f, err := parseSubmitArgs(tt.args)
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
			if d != tt.wantDeliv || f != tt.wantFile {
				t.Errorf("got (%q,%q), want (%q,%q)", d, f, tt.wantDeliv, tt.wantFile)
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

// fakeFetchCloner "clones" by writing the listed deliverable files into the
// target dir, then reads them back — stands in for a real git clone.
type fakeFetchCloner struct {
	files map[string][]byte // path -> content
}

func (f fakeFetchCloner) CloneRepo(authURL, instBranch, dir string) error {
	for path, content := range f.files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			return err
		}
	}
	return nil
}
func (f fakeFetchCloner) ReadFile(dir, path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, path))
}

// TestFetchDeliverables_HappyPath wires httptest multica (issue gitea-deliverables
// + credential) + a fake cloner and asserts the fetched content for a single
// issue and for the descendants case.
func TestFetchDeliverables_HappyPath(t *testing.T) {
	mkIssue := func(num int32, title string, depth int, owner, repo, inst string, delvs []issueGiteaDeliverableRef) issueGiteaDeliverable {
		return issueGiteaDeliverable{
			IssueID: "iss-" + title,
			Number:  num,
			Title:   title,
			Depth:   depth,
			Gitea: &issueGiteaContext{
				Owner: owner, Repo: repo,
				CloneURL:     "https://gitea.test/" + owner + "/" + repo + ".git",
				InstBranch:   inst,
				Deliverables: delvs,
			},
		}
	}
	multica := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/gitea/credential":
			jsonResponse(w, 200, map[string]string{"base_url": "https://gitea.test", "token": "pat-xyz"})
		case r.URL.Path == "/api/daemon/issues/MUL-1/gitea-deliverables":
			// Always returns self + child + grandchild (descendants always on).
			jsonResponse(w, 200, issueGiteaDeliverablesResponse{Issues: []issueGiteaDeliverable{
				mkIssue(1, "Self", 0, "t-ws", "wf-self", "inst-self",
					[]issueGiteaDeliverableRef{{NodeTitle: "NodeA", DeliverableID: "d1", Title: "Doc1", Path: "nodes/a/d1.md"}}),
				mkIssue(2, "Child", 1, "t-ws", "wf-child", "inst-child",
					[]issueGiteaDeliverableRef{{NodeTitle: "NodeB", DeliverableID: "d2", Title: "Doc2", Path: "nodes/b/d2.md"}}),
				mkIssue(3, "Grandchild", 2, "t-ws", "wf-grand", "inst-grand",
					[]issueGiteaDeliverableRef{{NodeTitle: "NodeC", DeliverableID: "d3", Title: "Doc3", Path: "nodes/c/d3.md"}}),
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer multica.Close()

	t.Setenv("MULTICA_TOKEN", "tok")
	t.Setenv("MULTICA_SERVER_URL", multica.URL)
	t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")

	cloner := fakeFetchCloner{files: map[string][]byte{
		"nodes/a/d1.md": []byte("# self doc"),
		"nodes/b/d2.md": []byte("# child doc"),
		"nodes/c/d3.md": []byte("# grandchild doc"),
	}}

	t.Run("fetch returns self + descendants with issue IDs", func(t *testing.T) {
		var out bytes.Buffer
		if err := fetchDeliverables(fetchConfig{issue: "MUL-1", cloner: cloner, out: &out}); err != nil {
			t.Fatalf("fetchDeliverables: %v", err)
		}
		s := out.String()
		// All three deliverable bodies present.
		for _, want := range []string{"self doc", "child doc", "grandchild doc"} {
			if !strings.Contains(s, want) {
				t.Errorf("expected %q in output, got:\n%s", want, s)
			}
		}
		// Each issue block carries its issue_id (so child-issue deliverables
		// are identifiable — the agent can re-fetch a specific child by ID).
		for _, id := range []string{"iss-Self", "iss-Child", "iss-Grandchild"} {
			if !strings.Contains(s, id) {
				t.Errorf("expected issue_id %q in output, got:\n%s", id, s)
			}
		}
		// Depth labels.
		for _, d := range []string{"depth 0", "depth 1", "depth 2"} {
			if !strings.Contains(s, d) {
				t.Errorf("expected %q label, got:\n%s", d, s)
			}
		}
	})

	t.Run("no deliverables", func(t *testing.T) {
		var out bytes.Buffer
		err := fetchDeliverables(fetchConfig{issue: "MUL-999", cloner: cloner, out: &out})
		if err == nil {
			t.Fatal("expected error for issue with no deliverables")
		}
	})
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
