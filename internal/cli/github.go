package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// submitGithubPR handles the GitHub code-PR path: pushes the current worktree
// branch, opens a GitHub PR, and reports to the server's submit endpoint.
// Mirrors submitGitlabMR (gitlab.go) but targets GitHub's REST API.
func submitGithubPR(cfg submitConfig) error {
	ctx := context.Background()

	cred, err := readGithubCredential()
	if err != nil {
		return fmt.Errorf("github credential: %w", err)
	}

	worktree, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	nodeRunID := os.Getenv("CS_CLOUD_NODE_RUN_ID")
	if nodeRunID == "" {
		return fmt.Errorf("CS_CLOUD_NODE_RUN_ID not set")
	}

	serverURL := envOr("CS_CLOUD_BACKEND_URL", "")
	if serverURL == "" {
		return fmt.Errorf("CS_CLOUD_BACKEND_URL not set")
	}
	token := os.Getenv("CS_CLOUD_TOKEN")

	currentBranch, err := cfg.gitOps.CurrentBranch(worktree)
	if err != nil {
		return fmt.Errorf("current branch: %w", err)
	}

	fmt.Fprintf(os.Stderr, "deliverable %s: submitting GitHub PR node_run=%s branch=%s\n", cfg.deliverableID, nodeRunID, currentBranch)

	if _, _, err := githubRepoFromURL(cfg.repoURL); err != nil {
		return fmt.Errorf("invalid GitHub repo URL: %w", err)
	}
	authURL := injectGithubTokenIntoURL(cfg.repoURL, cred.Token)
	if authURL == "" {
		return fmt.Errorf("invalid GitHub repo URL: cannot inject token into %q", cfg.repoURL)
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: pushing PR branch=%s\n", cfg.deliverableID, currentBranch)
	if err := cfg.gitOps.Push(worktree, authURL, currentBranch); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	targetBranch := envOr("CS_CLOUD_GITHUB_TARGET_BRANCH", "main")
	title := "deliverable " + cfg.deliverableID
	fmt.Fprintf(os.Stderr, "deliverable %s: opening PR source=%s target=%s\n", cfg.deliverableID, currentBranch, targetBranch)
	prURL, err := openGithubPR(ctx, cred.BaseURL, cred.Token, cfg.repoURL, currentBranch, targetBranch, title)
	if err != nil {
		return fmt.Errorf("open PR: %w", err)
	}

	submitEndpoint := serverURL + "/api/node-runs/" + nodeRunID + "/deliverables/" + cfg.deliverableID + "/submit"
	fmt.Fprintf(os.Stderr, "deliverable %s: reporting PR\n", cfg.deliverableID)
	if err := reportToServer(ctx, serverURL, token, submitEndpoint, prURL, os.Getenv("CS_CLOUD_WORKSPACE_ID"), os.Getenv("CS_CLOUD_AGENT_ID"), os.Getenv("CS_CLOUD_TASK_ID")); err != nil {
		return fmt.Errorf("report submit: %w", err)
	}

	fmt.Fprintf(os.Stderr, "deliverable %s: submitted pr=%s\n", cfg.deliverableID, prURL)
	fmt.Println(prURL)
	return nil
}

// readGithubCredential reads CS_CLOUD_GITHUB_TOKEN and the API base URL
// (CS_CLOUD_GITHUB_API_BASE, set by multica dispatch). Falls back to
// https://api.github.com.
func readGithubCredential() (*gitlabCredential, error) {
	token := strings.TrimSpace(os.Getenv("CS_CLOUD_GITHUB_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("CS_CLOUD_GITHUB_TOKEN not set (the task payload must provide the GitHub PAT)")
	}
	baseURL := strings.TrimSpace(envOr("CS_CLOUD_GITHUB_API_BASE", "https://api.github.com"))
	return &gitlabCredential{
		BaseURL: baseURL,
		Token:   token,
	}, nil
}

// githubAPIBase derives the GitHub REST API base from a repo URL host.
// github.com -> https://api.github.com; GHE -> https://<host>/api/v3.
func githubAPIBase(repoURL string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || u.Host == "" {
		return "https://api.github.com"
	}
	if u.Host == "github.com" {
		return "https://api.github.com"
	}
	return fmt.Sprintf("https://%s/api/v3", u.Host)
}

// openGithubPR POSTs /repos/{owner}/{repo}/pulls and returns html_url.
// On 422/409 (duplicate), resolves the existing PR via findExistingGithubPR.
func openGithubPR(ctx context.Context, base, token, repoURL, sourceBranch, targetBranch, title string) (string, error) {
	effectiveBase := base
	if effectiveBase == "" || effectiveBase == "https://api.github.com" {
		effectiveBase = githubAPIBase(repoURL)
	}

	owner, repo, err := githubRepoFromURL(repoURL)
	if err != nil {
		return "", err
	}
	project := owner + "/" + repo

	body, _ := json.Marshal(map[string]string{
		"title": title,
		"head":  sourceBranch,
		"base":  targetBranch,
	})
	endpoint := strings.TrimRight(effectiveBase, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build create PR request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create PR request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusUnprocessableEntity || resp.StatusCode == http.StatusConflict {
		return findExistingGithubPR(ctx, effectiveBase, token, project, sourceBranch, targetBranch)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github create PR: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return "", fmt.Errorf("parse PR response: %w", err)
	}
	if strings.TrimSpace(pr.HTMLURL) == "" {
		return "", fmt.Errorf("github create PR: response missing html_url: %s", strings.TrimSpace(string(respBody)))
	}
	return pr.HTMLURL, nil
}

// findExistingGithubPR lists open PRs filtered by head branch and returns the
// first match's html_url. Idempotent across retries.
func findExistingGithubPR(ctx context.Context, base, token, project, sourceBranch, targetBranch string) (string, error) {
	owner, repo, ok := strings.Cut(project, "/")
	if !ok || strings.TrimSpace(owner) == "" {
		return "", fmt.Errorf("cannot extract owner from GitHub project %q", project)
	}
	headFilter := owner + ":" + sourceBranch
	endpoint := fmt.Sprintf("%s/repos/%s/pulls?state=open&head=%s&base=%s",
		strings.TrimRight(base, "/"), url.PathEscape(owner)+"/"+url.PathEscape(repo),
		url.QueryEscape(headFilter), url.QueryEscape(targetBranch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build list PR request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("list PR request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("github list PRs: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var prs []struct {
		HTMLURL string `json:"html_url"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(respBody, &prs); err != nil {
		return "", fmt.Errorf("parse PR list: %w", err)
	}
	for _, pr := range prs {
		if pr.Head.Ref == sourceBranch && strings.TrimSpace(pr.HTMLURL) != "" {
			return pr.HTMLURL, nil
		}
	}
	return "", fmt.Errorf("github create PR returned error but no open PR for head %q", sourceBranch)
}

func githubRepoFromURL(repoURL string) (owner, repo string, err error) {
	raw := strings.TrimSpace(repoURL)
	if raw == "" {
		return "", "", fmt.Errorf("repo URL is empty")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse repo URL %q: %w", repoURL, err)
	}
	if u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", "", fmt.Errorf("repo URL must be an absolute HTTP(S) URL, got %q", repoURL)
	}
	project := strings.Trim(u.Path, "/")
	project = strings.TrimSuffix(project, ".git")
	parts := strings.Split(project, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", "", fmt.Errorf("cannot extract owner/repo from repo URL %q", repoURL)
	}
	return parts[0], parts[1], nil
}
