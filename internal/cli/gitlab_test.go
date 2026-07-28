package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestReadGitlabCredential_Valid confirms the happy path returns both fields.
func TestReadGitlabCredential_Valid(t *testing.T) {
	t.Setenv("MULTICA_GITLAB_TOKEN", "gl-pat")
	t.Setenv("MULTICA_GITLAB_BASE_URL", "https://gitlab.test")

	cred, err := readGitlabCredential()
	if err != nil {
		t.Fatalf("readGitlabCredential: %v", err)
	}
	if cred.Token != "gl-pat" {
		t.Errorf("Token = %q, want gl-pat", cred.Token)
	}
	if cred.BaseURL != "https://gitlab.test" {
		t.Errorf("BaseURL = %q, want https://gitlab.test", cred.BaseURL)
	}
}

// TestReadGitlabCredential_MissingToken requires the PAT up front.
func TestReadGitlabCredential_MissingToken(t *testing.T) {
	t.Setenv("MULTICA_GITLAB_TOKEN", "")
	t.Setenv("MULTICA_GITLAB_BASE_URL", "https://gitlab.test")

	if _, err := readGitlabCredential(); err == nil {
		t.Fatal("expected error when MULTICA_GITLAB_TOKEN is empty")
	}
}

// TestReadGitlabCredential_InvalidBaseURL verifies the base URL is validated as
// an absolute HTTP(S) URL BEFORE the worktree branch is pushed. Without this, a
// missing/malformed value fails after the push, orphaning the branch. CodeRabbit
// PR #27 (Critical).
func TestReadGitlabCredential_InvalidBaseURL(t *testing.T) {
	t.Setenv("MULTICA_GITLAB_TOKEN", "gl-pat")
	cases := []string{
		"",                 // missing
		"   ",              // whitespace only
		"gitlab.test",      // no scheme → url.Parse yields empty scheme/host
		"ftp://gitlab.test", // unsupported scheme
		"/local/path",      // path, not absolute URL
	}
	for _, baseURL := range cases {
		t.Setenv("MULTICA_GITLAB_BASE_URL", baseURL)
		if _, err := readGitlabCredential(); err == nil {
			t.Errorf("base URL %q: expected error, got nil", baseURL)
		}
	}
}

// gitlabMRServer is a tiny httptest harness for openGitlabMR: it records the
// create-MR POST and can serve a list-MR GET for the 409 recovery path.
type gitlabMRServer struct {
	createStatus int
	createBody   any
	// listResult is returned by GET /merge_requests when create returns 409.
	listResult []any
	createHits int
	listHits   int
}

func (g *gitlabMRServer) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "merge_requests"):
			g.createHits++
			if g.createStatus == 0 {
				g.createStatus = http.StatusCreated
			}
			if g.createStatus == http.StatusConflict {
				// List endpoint must be queried to recover the existing MR.
				if g.listResult == nil {
					g.listResult = []any{
						map[string]string{"web_url": "https://gitlab.test/g/repo/-/merge_requests/7"},
					}
				}
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "already exists"})
				return
			}
			jsonResponse(w, g.createStatus, g.createBody)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "merge_requests"):
			g.listHits++
			jsonResponse(w, http.StatusOK, g.listResult)
		default:
			http.NotFound(w, r)
		}
	})
}

// TestOpenGitlabMR_HappyPath confirms a 201 response returns web_url verbatim.
func TestOpenGitlabMR_HappyPath(t *testing.T) {
	g := &gitlabMRServer{
		createBody: map[string]string{"web_url": "https://gitlab.test/g/repo/-/merge_requests/42"},
	}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()

	url, err := openGitlabMR(context.Background(), srv.URL, "gl-pat",
		"https://gitlab.test/g/repo.git", "feat/x", "main", "title")
	if err != nil {
		t.Fatalf("openGitlabMR: %v", err)
	}
	if url != "https://gitlab.test/g/repo/-/merge_requests/42" {
		t.Errorf("url = %q, want the MR web_url", url)
	}
	if g.createHits != 1 || g.listHits != 0 {
		t.Errorf("createHits=%d listHits=%d, want 1/0 (no list on happy path)", g.createHits, g.listHits)
	}
}

// TestOpenGitlabMR_Duplicate409ResolvesExisting verifies the retry-safety path:
// when create returns 409 (MR already exists, e.g. reportToServer failed on a
// prior run), openGitlabMR resolves the existing MR's web_url via the list
// endpoint instead of failing. CodeRabbit PR #27 (Major).
func TestOpenGitlabMR_Duplicate409ResolvesExisting(t *testing.T) {
	g := &gitlabMRServer{
		createStatus: http.StatusConflict,
		listResult: []any{
			map[string]string{"web_url": "https://gitlab.test/g/repo/-/merge_requests/7"},
		},
	}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()

	url, err := openGitlabMR(context.Background(), srv.URL, "gl-pat",
		"https://gitlab.test/g/repo.git", "feat/x", "main", "title")
	if err != nil {
		t.Fatalf("openGitlabMR on 409: %v", err)
	}
	if url != "https://gitlab.test/g/repo/-/merge_requests/7" {
		t.Errorf("url = %q, want the EXISTING MR web_url recovered via list", url)
	}
	if g.listHits != 1 {
		t.Errorf("listHits = %d, want 1 (409 must trigger a list to recover the MR)", g.listHits)
	}
}

// TestOpenGitlabMR_Duplicate409ButNoExistingMR verifies that a 409 with no
// resolvable existing MR still surfaces an error rather than returning "".
func TestOpenGitlabMR_Duplicate409ButNoExistingMR(t *testing.T) {
	g := &gitlabMRServer{
		createStatus: http.StatusConflict,
		listResult:   []any{}, // GitLab says conflict but lists nothing
	}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()

	if _, err := openGitlabMR(context.Background(), srv.URL, "gl-pat",
		"https://gitlab.test/g/repo.git", "feat/x", "main", "title"); err == nil {
		t.Fatal("expected error when 409 has no resolvable existing MR, got nil")
	}
}

// TestOpenGitlabMR_RejectsEmptyWebURL verifies a 2xx response that omits
// web_url is rejected rather than reported as an empty URL upstream. CodeRabbit
// PR #27 (Minor).
func TestOpenGitlabMR_RejectsEmptyWebURL(t *testing.T) {
	g := &gitlabMRServer{
		createStatus: http.StatusCreated,
		createBody:   map[string]string{"web_url": "   "}, // whitespace-only
	}
	srv := httptest.NewServer(g.handler(t))
	defer srv.Close()

	if _, err := openGitlabMR(context.Background(), srv.URL, "gl-pat",
		"https://gitlab.test/g/repo.git", "feat/x", "main", "title"); err == nil {
		t.Fatal("expected error when MR response has empty web_url, got nil")
	}
}
