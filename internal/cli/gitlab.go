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

// submitGitlabMR handles the --mr (GitLab code MR) path: pushes the current
// worktree branch, opens a GitLab MR, and reports to the server's submit endpoint.
// The agent already wrote + committed its code in the code-repo worktree (the
// --mr flow does NOT pass --file); this function only pushes + opens the MR.
func submitGitlabMR(cfg submitConfig) error {
	ctx := context.Background()

	cred, err := readGitlabCredential()
	if err != nil {
		return fmt.Errorf("gitlab credential: %w", err)
	}

	// The agent cloned the code repo itself (git clone) and runs this command
	// from inside it, so the repo dir is the current working directory.
	worktree, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	nodeRunID := os.Getenv("CS_CLOUD_NODE_RUN_ID")
	if nodeRunID == "" {
		return fmt.Errorf("CS_CLOUD_NODE_RUN_ID not set")
	}

	// Validate the report-back URL BEFORE pushing/opening the MR: otherwise a
	// missing CS_CLOUD_BACKEND_URL leaves an orphaned MR on GitLab that no retry
	// can recover (the second push would hit "merge request already exists").
	serverURL := envOr("CS_CLOUD_BACKEND_URL", "")
	if serverURL == "" {
		return fmt.Errorf("CS_CLOUD_BACKEND_URL not set")
	}
	token := os.Getenv("CS_CLOUD_TOKEN")

	// A code MR needs the repo URL the agent passed via --repo. An empty value
	// means this path was reached without one (e.g. a document submit that the
	// caller failed to keep on the Gitea delivery path) — fail with an
	// actionable error instead of shelling out to `git push --force '' <branch>`
	// and surfacing git's opaque "bad repository ''" / exit 128.
	if strings.TrimSpace(cfg.repoURL) == "" {
		return fmt.Errorf("code MR submit requires --repo <url> (repo URL is empty)")
	}

	// Determine current branch in the worktree.
	currentBranch, err := cfg.gitOps.CurrentBranch(worktree)
	if err != nil {
		return fmt.Errorf("current branch: %w", err)
	}

	fmt.Fprintf(os.Stderr, "deliverable %s: submitting MR node_run=%s branch=%s\n", cfg.deliverableID, nodeRunID, currentBranch)

	deliverableID := cfg.deliverableID
	if deliverableID == "" {
		deliverableID, err = createAgentDefinedDeliverable(ctx, serverURL, token, nodeRunID, cfg.title, "", os.Getenv("CS_CLOUD_WORKSPACE_ID"), os.Getenv("CS_CLOUD_AGENT_ID"), os.Getenv("CS_CLOUD_TASK_ID"))
		if err != nil {
			return fmt.Errorf("create deliverable: %w", err)
		}
		fmt.Fprintf(os.Stderr, "deliverable: created agent-defined id=%s title=%q\n", deliverableID, cfg.title)
	}

	// Push current branch to the repo.
	authURL := injectTokenIntoURL(cfg.repoURL, cred.Token)
	fmt.Fprintf(os.Stderr, "deliverable %s: pushing MR branch=%s\n", cfg.deliverableID, currentBranch)
	if err := cfg.gitOps.Push(worktree, authURL, currentBranch); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	targetBranch := envOr("CS_CLOUD_GITLAB_TARGET_BRANCH", "")
	if targetBranch == "" {
		targetBranch = fetchGitlabDefaultBranch(ctx, cred.BaseURL, cred.Token, cfg.repoURL)
	}
	if targetBranch == "" {
		targetBranch = "main" // repo query failed — last-resort default
	}
	title := deliverableTitle(cfg.title, "deliverable "+cfg.deliverableID)
	fmt.Fprintf(os.Stderr, "deliverable %s: opening MR source=%s target=%s\n", cfg.deliverableID, currentBranch, targetBranch)
	mrURL, err := openGitlabMR(ctx, cred.BaseURL, cred.Token, cfg.repoURL, currentBranch, targetBranch, title)
	if err != nil {
		return fmt.Errorf("open MR: %w", err)
	}

	submitEndpoint, submitBodyField := resolveSubmitTarget(deliverableID, serverURL, nodeRunID)
	fmt.Fprintf(os.Stderr, "deliverable %s: reporting MR\n", deliverableID)
	if err := reportToServer(ctx, serverURL, token, submitEndpoint, mrURL, os.Getenv("CS_CLOUD_WORKSPACE_ID"), os.Getenv("CS_CLOUD_AGENT_ID"), os.Getenv("CS_CLOUD_TASK_ID"), submitBodyField); err != nil {
		return fmt.Errorf("report submit: %w", err)
	}

	fmt.Fprintf(os.Stderr, "deliverable %s: submitted mr=%s\n", deliverableID, mrURL)
	fmt.Println(mrURL)
	return nil
}

// gitlabCredential holds the GitLab PAT and base URL.
type gitlabCredential struct {
	BaseURL string
	Token   string
}

// readGitlabCredential reads CS_CLOUD_GITLAB_TOKEN and CS_CLOUD_GITLAB_BASE_URL.
// The base URL is validated as an absolute HTTP(S) URL up front so a missing/
// malformed value fails BEFORE the worktree branch is pushed — otherwise the
// branch is orphaned on GitLab and no retry can recover (the second attempt
// hits "merge request already exists"). CodeRabbit PR #27 (Critical).
func readGitlabCredential() (*gitlabCredential, error) {
	token := strings.TrimSpace(os.Getenv("CS_CLOUD_GITLAB_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("CS_CLOUD_GITLAB_TOKEN not set (the task payload must provide the GitLab PAT)")
	}
	baseURL := strings.TrimSpace(os.Getenv("CS_CLOUD_GITLAB_BASE_URL"))
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("CS_CLOUD_GITLAB_BASE_URL must be an absolute HTTP(S) URL, got %q", baseURL)
	}
	return &gitlabCredential{
		BaseURL: baseURL,
		Token:   token,
	}, nil
}

// fetchGitlabDefaultBranch queries the project's default branch via GET
// /api/v4/projects/:project. Returns "" on any failure so the caller falls
// back to a hardcoded default rather than blocking MR creation.
func fetchGitlabDefaultBranch(ctx context.Context, base, token, repoURL string) string {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return ""
	}
	project := strings.Trim(u.Path, "/")
	project = strings.TrimSuffix(project, ".git")
	if project == "" {
		return ""
	}
	endpoint := strings.TrimRight(base, "/") + "/api/v4/projects/" + url.PathEscape(project)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	var info struct {
		DefaultBranch string `json:"default_branch"`
	}
	body, _ := io.ReadAll(resp.Body)
	if json.Unmarshal(body, &info) != nil {
		return ""
	}
	return strings.TrimSpace(info.DefaultBranch)
}

// openGitlabMR POSTs /api/v4/projects/<urlencoded>/merge_requests and returns web_url.
func openGitlabMR(ctx context.Context, base, token, repoURL, sourceBranch, targetBranch, title string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil {
		return "", fmt.Errorf("parse repo URL %q: %w", repoURL, err)
	}
	// Extract project path (e.g. "group/repo" from "/group/repo.git").
	project := strings.Trim(u.Path, "/")
	project = strings.TrimSuffix(project, ".git")
	if project == "" {
		return "", fmt.Errorf("cannot extract project from repo URL %q", repoURL)
	}

	body, _ := json.Marshal(map[string]string{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
	})
	endpoint := strings.TrimRight(base, "/") + "/api/v4/projects/" + url.PathEscape(project) + "/merge_requests"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		// A nil request (malformed endpoint) would panic on the Header.Set
		// below. Return the construction error instead. CodeRabbit PR #27.
		return "", fmt.Errorf("build create MR request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create MR request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	// 409 Conflict: an open MR already exists for these branches. This happens
	// when a prior run created the MR but reportToServer failed and the CLI is
	// retrying — resolve the existing MR's URL so the flow is idempotent instead
	// of failing and orphaning the report. CodeRabbit PR #27 (Major).
	if resp.StatusCode == http.StatusConflict {
		return findExistingGitlabMR(ctx, base, token, project, sourceBranch, targetBranch)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gitlab create MR: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var mr struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return "", fmt.Errorf("parse MR response: %w", err)
	}
	// Reject a 2xx response that omits web_url — reporting an empty URL upstream
	// leaves the deliverable unresolvable. CodeRabbit PR #27 (Minor).
	if strings.TrimSpace(mr.WebURL) == "" {
		return "", fmt.Errorf("gitlab create MR: response missing web_url: %s", strings.TrimSpace(string(respBody)))
	}
	return mr.WebURL, nil
}

// findExistingGitlabMR lists open MRs filtered by source/target branch and
// returns the first match's web_url. Used when create-MR returns 409 (the MR
// already exists) so a retry recovers the existing URL instead of failing —
// the create+report flow must be idempotent across retries. CodeRabbit PR #27.
func findExistingGitlabMR(ctx context.Context, base, token, project, sourceBranch, targetBranch string) (string, error) {
	endpoint := fmt.Sprintf("%s/api/v4/projects/%s/merge_requests?source_branch=%s&target_branch=%s&state=opened",
		strings.TrimRight(base, "/"), url.PathEscape(project),
		url.QueryEscape(sourceBranch), url.QueryEscape(targetBranch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("build list MR request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", token)
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("list MR request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gitlab list MR: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var mrs []struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(respBody, &mrs); err != nil {
		return "", fmt.Errorf("parse MR list response: %w", err)
	}
	if len(mrs) == 0 {
		return "", fmt.Errorf("gitlab create MR returned 409 but no open MR for %s..%s", sourceBranch, targetBranch)
	}
	if strings.TrimSpace(mrs[0].WebURL) == "" {
		return "", fmt.Errorf("gitlab existing MR missing web_url")
	}
	return mrs[0].WebURL, nil
}
