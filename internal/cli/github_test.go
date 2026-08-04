package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadGithubCredential_Valid(t *testing.T) {
	t.Setenv("CS_CLOUD_GITHUB_TOKEN", "ghp-test")
	t.Setenv("CS_CLOUD_GITHUB_API_BASE", "https://api.github.com")

	cred, err := readGithubCredential()
	if err != nil {
		t.Fatalf("readGithubCredential: %v", err)
	}
	if cred.Token != "ghp-test" {
		t.Errorf("Token = %q, want ghp-test", cred.Token)
	}
	if cred.BaseURL != "https://api.github.com" {
		t.Errorf("BaseURL = %q, want https://api.github.com", cred.BaseURL)
	}
}

func TestReadGithubCredential_DefaultBase(t *testing.T) {
	t.Setenv("CS_CLOUD_GITHUB_TOKEN", "ghp-default")
	t.Setenv("CS_CLOUD_GITHUB_API_BASE", "")

	cred, err := readGithubCredential()
	if err != nil {
		t.Fatalf("readGithubCredential: %v", err)
	}
	if cred.BaseURL != "https://api.github.com" {
		t.Errorf("BaseURL = %q, want default https://api.github.com", cred.BaseURL)
	}
}

// TestFetchGithubDefaultBranch verifies the repo default branch is queried from
// GET /repos/{owner}/{repo} when no target branch is explicitly configured.
func TestFetchGithubDefaultBranch(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"default_branch":"develop"}`)
	}))
	defer srv.Close()

	got := fetchGithubDefaultBranch(context.Background(), srv.URL, "ghp-x", "https://github.com/o/r.git")
	if got != "develop" {
		t.Errorf("default branch = %q, want develop", got)
	}
	if !strings.HasSuffix(gotPath, "/repos/o/r") {
		t.Errorf("GET path = %q, want .../repos/o/r", gotPath)
	}
	if gotAuth != "token ghp-x" {
		t.Errorf("Authorization = %q, want 'token ghp-x'", gotAuth)
	}
}

// TestFetchGithubDefaultBranch_FallbackOn404 verifies a failed query returns ""
// so the caller falls back to the hardcoded default instead of erroring.
func TestFetchGithubDefaultBranch_FallbackOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if got := fetchGithubDefaultBranch(context.Background(), srv.URL, "ghp-x", "https://github.com/o/r.git"); got != "" {
		t.Errorf("default branch = %q, want \"\" (caller falls back)", got)
	}
}

func TestReadGithubCredential_MissingToken(t *testing.T) {
	t.Setenv("CS_CLOUD_GITHUB_TOKEN", "")
	if _, err := readGithubCredential(); err == nil {
		t.Fatal("expected error when CS_CLOUD_GITHUB_TOKEN is empty")
	}
}

func TestGithubAPIBase(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{"github.com SaaS", "https://github.com/org/repo.git", "https://api.github.com"},
		{"GHE", "https://ghe.example.com/org/repo.git", "https://ghe.example.com/api/v3"},
		{"empty", "", "https://api.github.com"},
		{"bare host", "github.com/org/repo", "https://api.github.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := githubAPIBase(tt.url); got != tt.want {
				t.Errorf("githubAPIBase(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

func TestOpenGithubPR_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/pulls") {
			if got := r.Header.Get("Authorization"); !strings.Contains(got, "token ghp-pat") {
				t.Errorf("Authorization = %q, want token ghp-pat", got)
			}
			jsonResponse(w, 201, map[string]any{"html_url": "https://github.com/org/repo/pulls/42", "number": 42})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	got, err := openGithubPR(context.Background(), srv.URL, "ghp-pat",
		"https://github.com/org/repo.git", "feat/x", "main", "test PR")
	if err != nil {
		t.Fatalf("openGithubPR: %v", err)
	}
	if got != "https://github.com/org/repo/pulls/42" {
		t.Errorf("url = %q, want the PR html_url", got)
	}
}

func TestOpenGithubPR_DuplicateResolvesExisting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/pulls"):
			jsonResponse(w, 422, map[string]any{"message": "Validation Failed"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls"):
			if got := r.URL.Query().Get("head"); got != "org:feat/x" {
				t.Errorf("head query = %q, want org:feat/x", got)
			}
			if got := r.URL.Query().Get("base"); got != "main" {
				t.Errorf("base query = %q, want main", got)
			}
			jsonResponse(w, 200, []map[string]any{{
				"html_url": "https://github.com/org/repo/pulls/7",
				"head":     map[string]string{"ref": "feat/x"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := openGithubPR(context.Background(), srv.URL, "ghp-pat",
		"https://github.com/org/repo.git", "feat/x", "main", "test PR")
	if err != nil {
		t.Fatalf("openGithubPR on 422: %v", err)
	}
	if got != "https://github.com/org/repo/pulls/7" {
		t.Errorf("url = %q, want the EXISTING PR html_url", got)
	}
}

func TestOpenGithubPR_DuplicateResolvesExistingOnConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/pulls"):
			jsonResponse(w, 409, map[string]any{"message": "Conflict"})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/pulls"):
			if got := r.URL.Query().Get("head"); got != "org:feat/conflict" {
				t.Errorf("head query = %q, want org:feat/conflict", got)
			}
			if got := r.URL.Query().Get("base"); got != "main" {
				t.Errorf("base query = %q, want main", got)
			}
			jsonResponse(w, 200, []map[string]any{{
				"html_url": "https://github.com/org/repo/pulls/9",
				"head":     map[string]string{"ref": "feat/conflict"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	got, err := openGithubPR(context.Background(), srv.URL, "ghp-pat",
		"https://github.com/org/repo.git", "feat/conflict", "main", "test PR")
	if err != nil {
		t.Fatalf("openGithubPR on 409: %v", err)
	}
	if got != "https://github.com/org/repo/pulls/9" {
		t.Errorf("url = %q, want the EXISTING PR html_url", got)
	}
}

func TestOpenGithubPR_RejectsEmptyHTMLURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 201, map[string]any{"html_url": ""})
	}))
	defer srv.Close()

	if _, err := openGithubPR(context.Background(), srv.URL, "ghp-pat",
		"https://github.com/org/repo.git", "feat/x", "main", "test PR"); err == nil {
		t.Fatal("expected error when PR response has empty html_url, got nil")
	}
}

func TestOpenGithubPR_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, 500, map[string]any{"message": "internal server error"})
	}))
	defer srv.Close()

	if _, err := openGithubPR(context.Background(), srv.URL, "ghp-pat",
		"https://github.com/org/repo.git", "feat/x", "main", "test PR"); err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}

func TestOpenGithubPR_BadRepoURL(t *testing.T) {
	if _, err := openGithubPR(context.Background(), "https://api.github.com", "token",
		"", "feat/x", "main", "test PR"); err == nil {
		t.Fatal("expected error for empty repo URL, got nil")
	}
}

func TestSubmitGithubPRRejectsBadRepoURLBeforePush(t *testing.T) {
	t.Setenv("CS_CLOUD_GITHUB_TOKEN", "ghp-test")
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "node-run-1")
	t.Setenv("CS_CLOUD_BACKEND_URL", "https://backend.test")

	ops := &fakeGitOps{currentBranch: "feat/x"}
	err := submitGithubPR(submitConfig{
		deliverableID: "d1",
		repoURL:       "not-a-url",
		gitOps:        ops,
	})
	if err == nil {
		t.Fatal("expected invalid repo URL error")
	}
	if !strings.Contains(err.Error(), "invalid GitHub repo URL") {
		t.Fatalf("error = %v, want invalid GitHub repo URL", err)
	}
	if len(ops.pushCalls) != 0 {
		t.Fatalf("Push called for invalid repo URL: %+v", ops.pushCalls)
	}
}

func TestSubmitGithubPRRejectsHTTPRepoURLBeforePush(t *testing.T) {
	t.Setenv("CS_CLOUD_GITHUB_TOKEN", "ghp-test")
	t.Setenv("CS_CLOUD_NODE_RUN_ID", "node-run-1")
	t.Setenv("CS_CLOUD_BACKEND_URL", "https://backend.test")

	ops := &fakeGitOps{currentBranch: "feat/x"}
	err := submitGithubPR(submitConfig{
		deliverableID: "d1",
		repoURL:       "http://github.com/org/repo.git",
		gitOps:        ops,
	})
	if err == nil {
		t.Fatal("expected invalid repo URL error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "https") {
		t.Fatalf("error = %v, want https requirement", err)
	}
	if len(ops.pushCalls) != 0 {
		t.Fatalf("Push called for HTTP repo URL: %+v", ops.pushCalls)
	}
}
