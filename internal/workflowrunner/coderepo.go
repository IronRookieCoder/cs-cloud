package workflowrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
	"unicode"
)

// codeMRHTTPClient is bounded so a hung GitLab cannot stall the task driver.
var codeMRHTTPClient = &http.Client{Timeout: 30 * time.Second}

// codeRepoParts splits a git URL like
// "http://gitlab.local/root/costrict.git" into its base ("http://gitlab.local"),
// project path ("root/costrict"), and an authed clone/push URL with the token
// embedded. Returns ok=false for an unparseable URL.
func codeRepoParts(repoURL, token string) (base, project, authedURL string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Host == "" || u.Scheme == "" {
		return "", "", "", false
	}
	base = u.Scheme + "://" + u.Host
	project = strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	if project == "" {
		return "", "", "", false
	}
	authed := *u
	if token != "" {
		authed.User = url.UserPassword("oauth2", token)
	}
	authed.Path = u.Path
	if !strings.HasSuffix(authed.Path, ".git") {
		authed.Path += ".git"
	}
	return base, project, authed.String(), true
}

// OpenCodeMR commits any uncommitted agent changes in the worktree, pushes a
// source branch to the code repo, opens a GitLab merge request (source → the
// repo's default branch) and returns the MR web URL. The MR URL is meant to be
// folded into the task output so multica's worker-output parser files it as the
// pull_request deliverable.
//
// token is the workspace GitLab PAT (may be empty for public repos that allow
// anonymous push — rare). taskID disambiguates the source branch.
func OpenCodeMR(ctx context.Context, worktree, repoURL, token, taskID string) (string, error) {
	base, project, authedURL, ok := codeRepoParts(repoURL, token)
	if !ok {
		return "", fmt.Errorf("parse repo url %q", repoURL)
	}
	sourceBranch := "multica/" + sanitizeBranchSegment(taskID)

	// Carry both committed work on HEAD and any uncommitted agent edits onto the
	// source branch, then push it.
	if err := runGitCtx(ctx, "-C", worktree, "checkout", "-B", sourceBranch); err != nil {
		return "", fmt.Errorf("create source branch: %w", err)
	}
	_ = runGitCtx(ctx, "-C", worktree, "add", "-A")
	// Keep cs-cloud's own worktree metadata out of the code MR.
	_ = runGitCtx(ctx, "-C", worktree, "reset", "-q", "--", ".cs-workflow-ref")
	if worktreeHasStagedChanges(ctx, worktree) {
		_ = runGitCtx(ctx, "-C", worktree,
			"-c", "user.email=bot@multica", "-c", "user.name=Multica Bot",
			"commit", "-m", "multica workflow task "+taskID)
	}
	if err := runGitCtx(ctx, "-C", worktree, "push", "--force", authedURL, sourceBranch); err != nil {
		return "", fmt.Errorf("push source branch: %w", err)
	}

	target, err := defaultBranch(ctx, worktree, base, project, token)
	if err != nil {
		return "", fmt.Errorf("resolve default branch: %w", err)
	}
	if target == sourceBranch {
		return "", fmt.Errorf("source branch equals default branch %q", target)
	}

	mrURL, err := createGitlabMR(ctx, base, project, token, sourceBranch, target, taskID)
	if err != nil {
		return "", fmt.Errorf("open merge request: %w", err)
	}

	// Release the source branch: the worktree shares the workspace's mirror
	// cache, and leaving the branch checked out blocks the cache's `remote
	// update` for every subsequent task (git refuses to fetch into a branch
	// that is checked out). Detach + delete the local ref; the branch lives on
	// at the remote (we just pushed it) and the MR references it there.
	_ = runGitCtx(ctx, "-C", worktree, "checkout", "--detach", "HEAD")
	_ = runGitCtx(ctx, "-C", worktree, "branch", "-D", sourceBranch)
	return mrURL, nil
}

// worktreeHasStagedChanges reports whether the index differs from HEAD — i.e.
// `git add -A` staged something worth committing. `git diff --cached --quiet`
// exits 1 when there are staged differences, 0 when none.
func worktreeHasStagedChanges(ctx context.Context, worktree string) bool {
	return runGitCtx(ctx, "-C", worktree, "diff", "--cached", "--quiet") != nil
}

// defaultBranch resolves the repo's default branch via origin/HEAD, falling
// back to the GitLab project API.
func defaultBranch(ctx context.Context, worktree, base, project, token string) (string, error) {
	if out, err := gitOutput(ctx, "-C", worktree, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if b := strings.TrimSpace(out); b != "" {
			return b, nil
		}
	}
	// Fallback: GitLab project API.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/api/v4/projects/"+url.PathEscape(project), nil)
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := codeMRHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gitlab project api: status %d", resp.StatusCode)
	}
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		return "", err
	}
	if p.DefaultBranch == "" {
		p.DefaultBranch = "main"
	}
	return p.DefaultBranch, nil
}

// createGitlabMR POSTs a merge request and returns its web_url.
func createGitlabMR(ctx context.Context, base, project, token, source, target, taskID string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"source_branch": source,
		"target_branch": target,
		"title":         "multica workflow task " + taskID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		base+"/api/v4/projects/"+url.PathEscape(project)+"/merge_requests", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("PRIVATE-TOKEN", token)
	}
	resp, err := codeMRHTTPClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var mr struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return "", err
	}
	if mr.WebURL == "" {
		return "", fmt.Errorf("empty web_url in response")
	}
	return mr.WebURL, nil
}

// gitOutput runs git and returns trimmed stdout. A non-zero exit is surfaced as
// an error carrying stderr; callers that treat exit-1 as "differences exist"
// (e.g. git diff --quiet) can ignore the error when out is non-empty.
func gitOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("git %v: %w: %s", args, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// sanitizeBranchSegment keeps a task id (or other token) safe as a git branch
// suffix: lowercase, alnum + dashes only.
func sanitizeBranchSegment(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' {
			b.WriteRune(r)
		} else if r == ' ' || r == '_' || r == '/' {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "task"
	}
	// Cap length so we never blow past git's branch name limit.
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
