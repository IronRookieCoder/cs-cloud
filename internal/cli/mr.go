package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"cs-cloud/internal/app"
)

// mrHTTPClient has a bounded timeout for GitLab API calls.
var mrHTTPClient = &http.Client{Timeout: 30 * time.Second}

// mrCmd implements `cs-cloud mr <subcommand>`. Ports multica's cs-workflow
// `mr create` for code-type deliverables (GitLab merge requests).
func mrCmd(a *app.App, args []string) error {
	_ = a
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud mr create --source-branch <branch> --title \"...\" [--push]")
	}
	switch args[0] {
	case "create":
		return runMRCreate(args[1:])
	default:
		return fmt.Errorf("unknown mr command: %s", args[0])
	}
}

// runMRCreate: `cs-cloud mr create --source-branch <branch> --title "..."
// [--description "..."] [--target-branch main] [--push|--no-push]`
//
// Fetches the GitLab credential from multica, optionally pushes the source
// branch, finds the GitLab project, and creates a merge request. Prints the
// MR web URL on stdout.
func runMRCreate(args []string) error {
	sourceBranch, title, description, targetBranch, shouldPush, err := parseMRCreateArgs(args)
	if err != nil {
		return err
	}

	token := os.Getenv("MULTICA_TOKEN")
	serverURL := strings.TrimRight(envOr("MULTICA_SERVER_URL", ""), "/")
	workspaceID := os.Getenv("MULTICA_WORKSPACE_ID")
	if token == "" || serverURL == "" || workspaceID == "" {
		return fmt.Errorf("MULTICA_TOKEN, MULTICA_SERVER_URL, and MULTICA_WORKSPACE_ID are required")
	}

	cred, err := fetchGitlabCredential(serverURL, token, workspaceID)
	if err != nil {
		return err
	}

	gitlabBase := strings.TrimRight(cred.BaseURL, "/")
	remoteURL, err := getGitRemoteURL()
	if err != nil {
		return fmt.Errorf("detect remote URL: %w", err)
	}
	gitlabBase = deriveGitlabBase(remoteURL, gitlabBase)

	if shouldPush {
		if err := pushBranch(sourceBranch, buildAuthURL(remoteURL, cred.Token)); err != nil {
			return fmt.Errorf("push branch: %w", err)
		}
	}

	repoPath := strings.TrimSuffix(extractPathFromRemote(remoteURL), ".git")
	projectID, err := findProject(gitlabBase, cred.Token, repoPath)
	if err != nil {
		return err
	}

	mrURL, err := createMergeRequest(gitlabBase, cred.Token, projectID, sourceBranch, targetBranch, title, description)
	if err != nil {
		return err
	}
	fmt.Println(mrURL)
	return nil
}

func parseMRCreateArgs(args []string) (source, title, description, target string, push bool, err error) {
	target = "main"
	push = true
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--source-branch":
			if i+1 >= len(args) {
				err = fmt.Errorf("--source-branch needs a value")
				return
			}
			source = args[i+1]
			i += 2
		case "--title":
			if i+1 >= len(args) {
				err = fmt.Errorf("--title needs a value")
				return
			}
			title = args[i+1]
			i += 2
		case "--description":
			if i+1 >= len(args) {
				err = fmt.Errorf("--description needs a value")
				return
			}
			description = args[i+1]
			i += 2
		case "--target-branch":
			if i+1 >= len(args) {
				err = fmt.Errorf("--target-branch needs a value")
				return
			}
			target = args[i+1]
			i += 2
		case "--push":
			push = true
			i++
		case "--no-push":
			push = false
			i++
		default:
			err = fmt.Errorf("unknown argument: %s", args[i])
			return
		}
	}
	if source == "" {
		err = fmt.Errorf("--source-branch is required")
	} else if title == "" {
		err = fmt.Errorf("--title is required")
	}
	return
}

func fetchGitlabCredential(serverURL, token, workspaceID string) (struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}, error) {
	var cred struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
	}
	credReq, err := http.NewRequest(http.MethodGet, serverURL+"/api/gitlab/credential", nil)
	if err != nil {
		return cred, fmt.Errorf("build credential request: %w", err)
	}
	credReq.Header.Set("Authorization", "Bearer "+token)
	credReq.Header.Set("X-Workspace-ID", workspaceID)

	credResp, err := mrHTTPClient.Do(credReq)
	if err != nil {
		return cred, fmt.Errorf("fetch GitLab credential: %w", err)
	}
	defer credResp.Body.Close()
	if credResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(credResp.Body)
		return cred, fmt.Errorf("fetch GitLab credential: HTTP %d: %s", credResp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(credResp.Body).Decode(&cred); err != nil {
		return cred, fmt.Errorf("parse credential response: %w", err)
	}
	if cred.BaseURL == "" || cred.Token == "" {
		return cred, fmt.Errorf("invalid credential response: base_url and token are required")
	}
	return cred, nil
}

// getGitRemoteURL returns the fetch URL of the "origin" remote.
func getGitRemoteURL() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// deriveGitlabBase extracts the GitLab instance URL from a remote URL,
// falling back to the server-provided base URL.
func deriveGitlabBase(remoteURL, fallback string) string {
	base := extractBaseFromRemote(remoteURL)
	if base != "" {
		return base
	}
	return fallback
}

func extractBaseFromRemote(remoteURL string) string {
	if strings.HasPrefix(remoteURL, "https://") || strings.HasPrefix(remoteURL, "http://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%s://%s", u.Scheme, u.Host)
	}
	sshRe := regexp.MustCompile(`^git@([^:]+):(.+)$`)
	if m := sshRe.FindStringSubmatch(remoteURL); m != nil {
		return "https://" + m[1]
	}
	if strings.HasPrefix(remoteURL, "git://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return ""
		}
		return "https://" + u.Host
	}
	return ""
}

// buildAuthURL inserts the PAT into the remote URL for authenticated git push.
func buildAuthURL(remoteURL, token string) string {
	if !strings.HasPrefix(remoteURL, "http://") && !strings.HasPrefix(remoteURL, "https://") {
		base := extractBaseFromRemote(remoteURL)
		path := extractPathFromRemote(remoteURL)
		if base != "" && path != "" {
			return base + "/" + strings.TrimSuffix(path, ".git") + ".git"
		}
		return remoteURL
	}
	u, err := url.Parse(remoteURL)
	if err != nil {
		return remoteURL
	}
	u.User = url.UserPassword("oauth2", token)
	return u.String()
}

func extractPathFromRemote(remoteURL string) string {
	if strings.HasPrefix(remoteURL, "https://") || strings.HasPrefix(remoteURL, "http://") {
		u, err := url.Parse(remoteURL)
		if err != nil {
			return ""
		}
		return strings.TrimPrefix(u.Path, "/")
	}
	sshRe := regexp.MustCompile(`^git@[^:]+:(.+)$`)
	if m := sshRe.FindStringSubmatch(remoteURL); m != nil {
		return m[1]
	}
	return ""
}

// pushBranch pushes the source branch using the authenticated URL.
func pushBranch(branch, authURL string) error {
	fmt.Fprintf(os.Stderr, "Pushing %s...\n", branch)
	cmd := exec.Command("git", "push", authURL, branch)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git push: %w", err)
	}
	return nil
}

func findProject(gitlabBase, token, projectPath string) (int, error) {
	gitlabAPI := gitlabBase + "/api/v4"
	encodedPath := url.PathEscape(projectPath)
	lookupURL := fmt.Sprintf("%s/projects/%s", gitlabAPI, encodedPath)
	projReq, err := http.NewRequest(http.MethodGet, lookupURL, nil)
	if err != nil {
		return 0, fmt.Errorf("build project lookup request: %w", err)
	}
	projReq.Header.Set("PRIVATE-TOKEN", token)

	projResp, err := mrHTTPClient.Do(projReq)
	if err != nil {
		return 0, fmt.Errorf("lookup project: %w", err)
	}
	defer projResp.Body.Close()
	if projResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(projResp.Body)
		return 0, fmt.Errorf("lookup project: HTTP %d: %s", projResp.StatusCode, strings.TrimSpace(string(body)))
	}
	var project struct {
		ID int `json:"id"`
	}
	if err := json.NewDecoder(projResp.Body).Decode(&project); err != nil {
		return 0, fmt.Errorf("parse project response: %w", err)
	}
	return project.ID, nil
}

func createMergeRequest(gitlabBase, token string, projectID int, sourceBranch, targetBranch, title, description string) (string, error) {
	gitlabAPI := gitlabBase + "/api/v4"
	mrBody := map[string]string{
		"source_branch": sourceBranch,
		"target_branch": targetBranch,
		"title":         title,
	}
	if description != "" {
		mrBody["description"] = description
	}
	mrJSON, err := json.Marshal(mrBody)
	if err != nil {
		return "", fmt.Errorf("marshal MR body: %w", err)
	}
	mrURL := fmt.Sprintf("%s/projects/%d/merge_requests", gitlabAPI, projectID)
	mrReq, err := http.NewRequest(http.MethodPost, mrURL, strings.NewReader(string(mrJSON)))
	if err != nil {
		return "", fmt.Errorf("build MR create request: %w", err)
	}
	mrReq.Header.Set("PRIVATE-TOKEN", token)
	mrReq.Header.Set("Content-Type", "application/json")

	mrResp, err := mrHTTPClient.Do(mrReq)
	if err != nil {
		return "", fmt.Errorf("create merge request: %w", err)
	}
	defer mrResp.Body.Close()
	if mrResp.StatusCode < 200 || mrResp.StatusCode >= 300 {
		body, _ := io.ReadAll(mrResp.Body)
		return "", fmt.Errorf("create merge request: HTTP %d: %s", mrResp.StatusCode, strings.TrimSpace(string(body)))
	}
	var mrResult struct {
		WebURL string `json:"web_url"`
	}
	if err := json.NewDecoder(mrResp.Body).Decode(&mrResult); err != nil {
		return "", fmt.Errorf("parse MR response: %w", err)
	}
	return mrResult.WebURL, nil
}
