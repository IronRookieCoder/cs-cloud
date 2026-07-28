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

	"cs-cloud/internal/workflowrunner"
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

	// CS_CLOUD_WORKTREE is the TASK ROOT (task.go buildEnv sets it to the
	// taskRoot, NOT a per-repo worktree). The agent ran `cs-cloud repo checkout`
	// first, which created the code-repo worktree at <taskRoot>/<repoName>/.
	// Resolve that subdir via RepoWorktreeDir — the same helper CheckoutRepo
	// uses — so submit pushes from the exact worktree checkout created, not the
	// bare task root (which has no .git and would fail at git rev-parse).
	taskRoot := strings.TrimSpace(os.Getenv("CS_CLOUD_WORKTREE"))
	if taskRoot == "" {
		return fmt.Errorf("CS_CLOUD_WORKTREE not set")
	}
	worktree := workflowrunner.RepoWorktreeDir(taskRoot, cfg.repoURL)

	nodeRunID := os.Getenv("MULTICA_NODE_RUN_ID")
	if nodeRunID == "" {
		return fmt.Errorf("MULTICA_NODE_RUN_ID not set")
	}

	// Validate the report-back URL BEFORE pushing/opening the MR: otherwise a
	// missing MULTICA_SERVER_URL leaves an orphaned MR on GitLab that no retry
	// can recover (the second push would hit "merge request already exists").
	serverURL := envOr("MULTICA_SERVER_URL", "")
	if serverURL == "" {
		return fmt.Errorf("MULTICA_SERVER_URL not set")
	}
	token := os.Getenv("MULTICA_TOKEN")

	// Determine current branch in the worktree.
	currentBranch, err := cfg.gitOps.CurrentBranch(worktree)
	if err != nil {
		return fmt.Errorf("current branch: %w", err)
	}

	// Push current branch to the repo.
	authURL := injectTokenIntoURL(cfg.repoURL, cred.Token)
	if err := cfg.gitOps.Push(worktree, authURL, currentBranch); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	targetBranch := envOr("MULTICA_GITLAB_TARGET_BRANCH", "main")
	title := "deliverable " + cfg.deliverableID
	mrURL, err := openGitlabMR(ctx, cred.BaseURL, cred.Token, cfg.repoURL, currentBranch, targetBranch, title)
	if err != nil {
		return fmt.Errorf("open MR: %w", err)
	}

	submitEndpoint := serverURL + "/api/node-runs/" + nodeRunID + "/deliverables/" + cfg.deliverableID + "/submit"
	if err := reportToServer(ctx, serverURL, token, submitEndpoint, mrURL); err != nil {
		return fmt.Errorf("report submit: %w", err)
	}

	fmt.Println(mrURL)
	return nil
}

// gitlabCredential holds the GitLab PAT and base URL.
type gitlabCredential struct {
	BaseURL string
	Token   string
}

// readGitlabCredential reads MULTICA_GITLAB_TOKEN and MULTICA_GITLAB_BASE_URL.
func readGitlabCredential() (*gitlabCredential, error) {
	token := strings.TrimSpace(os.Getenv("MULTICA_GITLAB_TOKEN"))
	if token == "" {
		return nil, fmt.Errorf("MULTICA_GITLAB_TOKEN not set (the task payload must provide the GitLab PAT)")
	}
	return &gitlabCredential{
		BaseURL: strings.TrimSpace(os.Getenv("MULTICA_GITLAB_BASE_URL")),
		Token:   token,
	}, nil
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
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create MR request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gitlab create MR: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var mr struct {
		WebURL string `json:"web_url"`
	}
	if err := json.Unmarshal(respBody, &mr); err != nil {
		return "", fmt.Errorf("parse MR response: %w", err)
	}
	return mr.WebURL, nil
}
