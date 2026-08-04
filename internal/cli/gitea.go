package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cs-cloud/internal/app"
)

// deliverableCmd implements `cs-cloud workflow deliverable <subcommand>`. It is the cs-cloud home
// for the platform-Gitea document-deliverable operations migrated from
// the server's cs-workflow CLI. Subcommands run inside a workflow-node task
// context: they read CS_CLOUD_GITEA_* / CS_CLOUD_TOKEN / CS_CLOUD_BACKEND_URL env
// (pushed by the server in the task payload) and are invoked by the agent (csc)
// during a node run.
func deliverableCmd(a *app.App, args []string) error {
	_ = a // task-context command; uses task env, not daemon config/credentials
	if len(args) == 0 {
		printDeliverableUsage()
		return nil
	}
	switch args[0] {
	case "submit":
		return runGiteaSubmit(args[1:])
	case "help", "-h", "--help":
		printDeliverableUsage()
		return nil
	default:
		printDeliverableUsage()
		return fmt.Errorf("unknown gitea command: %s", args[0])
	}
}

func printDeliverableUsage() {
	fmt.Println(`deliverable - workflow deliverable operations

Usage:
  cs-cloud workflow deliverable submit --deliverable <id> --file <path> [--title <title>]
    Document/file deliverable: submit a pre-registered deliverable by id.
    Reads CS_CLOUD_GITEA_* env, uses CS_CLOUD_GITEA_TOKEN as the workspace bot
    PAT, pushes the document to the node branch, opens a Gitea PR (node->inst),
    and registers the PR URL back to the server.

  cs-cloud workflow deliverable submit --file <path> --title "<title>"
    Agent-defined document/file deliverable: the command creates the deliverable
    on the node run, then submits it through the Gitea document path.

  cs-cloud workflow deliverable submit --deliverable <id> --mr --repo <url> [--title <title>]
    Code MR/PR deliverable: run from inside the code repository after committing.
    The provider is selected by CS_CLOUD_CODE_PROVIDER and reads
    CS_CLOUD_GITLAB_TOKEN or CS_CLOUD_GITHUB_TOKEN to push/open the MR/PR, then
    registers the MR/PR URL back to the server.`)
}

// runGiteaSubmit parses flags and runs the submit flow.
func runGiteaSubmit(args []string) error {
	deliverableID, filePath, mrMode, repoURL, title, err := parseSubmitArgs(args)
	if err != nil {
		return err
	}
	return submitDeliverable(submitConfig{
		deliverableID: deliverableID,
		filePath:      filePath,
		mrMode:        mrMode,
		repoURL:       repoURL,
		title:         title,
		gitOps:        &execGitOps{},
	})
}

// parseSubmitArgs parses `--deliverable <id> --file <path> [--title <title>] [--mr --repo <url>]` from the flat arg
// slice (cs-cloud's dispatcher has no flag library, so we parse by hand).
func parseSubmitArgs(args []string) (deliverable, file string, mrMode bool, repoURL, title string, err error) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--deliverable":
			if i+1 >= len(args) {
				return "", "", false, "", "", fmt.Errorf("--deliverable needs a value")
			}
			deliverable = args[i+1]
			i += 2
		case "--file":
			if i+1 >= len(args) {
				return "", "", false, "", "", fmt.Errorf("--file needs a value")
			}
			file = args[i+1]
			i += 2
		case "--title":
			if i+1 >= len(args) {
				return "", "", false, "", "", fmt.Errorf("--title needs a value")
			}
			title = args[i+1]
			i += 2
		case "--mr":
			mrMode = true
			i++
		case "--repo":
			if i+1 >= len(args) {
				return "", "", false, "", "", fmt.Errorf("--repo needs a value")
			}
			repoURL = args[i+1]
			i += 2
		default:
			return "", "", false, "", "", fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	if mrMode || repoURL != "" {
		// Code PR/MR mode does not require --file: the agent already edited in worktree.
		if repoURL == "" {
			return "", "", false, "", "", fmt.Errorf("--repo is required with --mr")
		}
	} else {
		// Document mode: --file is required. --deliverable identifies a
		// pre-registered deliverable; when omitted, --title drives agent-defined
		// mode (the CLI creates the deliverable on submit).
		if file == "" {
			return "", "", false, "", "", fmt.Errorf("--file is required")
		}
		if deliverable == "" && strings.TrimSpace(title) == "" {
			return "", "", false, "", "", fmt.Errorf("--deliverable or --title is required")
		}
	}
	return deliverable, file, mrMode, repoURL, strings.TrimSpace(title), nil
}

// submitConfig parameterizes submitDeliverable for testing.
type submitConfig struct {
	deliverableID     string
	filePath          string
	gitOps            gitOps
	giteaBaseOverride string // test-only: override the Gitea base URL (else from credential)
	mrMode            bool   // --mr: code MR mode
	repoURL           string // --mr mode: code repository URL (agent worktree already checked out)
	title             string // optional PR/MR title supplied by the agent
}

// gitOps abstracts the git operations so the submit flow is unit-testable.
// The production impl (execGitOps) shells out to git.
type gitOps interface {
	Clone(authURL, branch, dir string) error
	WriteFile(dir, path string, content []byte) error
	HasChanges(dir, path string) (bool, error)
	Commit(dir, path, message string) error
	Push(dir, authURL, branch string) error
	CurrentBranch(dir string) (string, error)
}

type giteaContext struct {
	nodeRunID    string
	owner        string
	repo         string
	cloneURL     string // full <base>/<owner>/<repo>.git from the server (preferred over self-built)
	instBranch   string
	nodeBranch   string
	deliverables []giteaDeliverableRef
}

type giteaDeliverableRef struct {
	ID    string `json:"deliverable_id"`
	Title string `json:"title"`
	Path  string `json:"path"`
}

func readGiteaContext() (*giteaContext, error) {
	c := &giteaContext{
		nodeRunID:  os.Getenv("CS_CLOUD_NODE_RUN_ID"),
		owner:      os.Getenv("CS_CLOUD_GITEA_OWNER"),
		repo:       os.Getenv("CS_CLOUD_GITEA_REPO"),
		cloneURL:   os.Getenv("CS_CLOUD_GITEA_CLONE_URL"),
		instBranch: os.Getenv("CS_CLOUD_GITEA_INST_BRANCH"),
		nodeBranch: os.Getenv("CS_CLOUD_GITEA_NODE_BRANCH"),
	}
	if c.nodeRunID == "" {
		return nil, fmt.Errorf("CS_CLOUD_NODE_RUN_ID not set; this command must run inside a workflow-node task")
	}
	for _, f := range []string{c.owner, c.repo, c.instBranch, c.nodeBranch} {
		if f == "" {
			return nil, fmt.Errorf("CS_CLOUD_GITEA_* env incomplete (owner/repo/inst/node-branch required)")
		}
	}
	raw := os.Getenv("CS_CLOUD_GITEA_DELIVERABLES")
	if raw == "" {
		return c, nil
	}
	if err := json.Unmarshal([]byte(raw), &c.deliverables); err != nil {
		return nil, fmt.Errorf("parse CS_CLOUD_GITEA_DELIVERABLES: %w", err)
	}
	return c, nil
}

func (c *giteaContext) deliverablePath(id string) (string, error) {
	for _, d := range c.deliverables {
		if d.ID == id {
			return d.Path, nil
		}
	}
	return "", fmt.Errorf("deliverable %q not in CS_CLOUD_GITEA_DELIVERABLES", id)
}

// submitDeliverable is the testable core. Returns nil only after the PR/MR is
// registered back to the server.
func submitDeliverable(cfg submitConfig) error {
	// The submit *form* — not CS_CLOUD_CODE_PROVIDER — decides document vs
	// code. A --file submit is a Gitea document deliverable and must always
	// take the delivery path. CS_CLOUD_CODE_PROVIDER describes the workspace's
	// *code* repo, and multica injects it into every task in a workspace that
	// has a code repo — including document-only nodes. Checking the provider
	// env first rerouted a --file submit into submitGitlabMR, which then
	// pushed to an empty --repo (`git push --force '' <branch>`). Only a code
	// submit (--mr / --repo) honors the provider env.
	if cfg.mrMode || cfg.repoURL != "" {
		provider := strings.ToLower(strings.TrimSpace(os.Getenv("CS_CLOUD_CODE_PROVIDER")))
		switch provider {
		case "github":
			return submitGithubPR(cfg)
		case "gitlab":
			return submitGitlabMR(cfg)
		}
		// Backward compat: --mr (or --repo) without provider env implies gitlab.
		return submitGitlabMR(cfg)
	}

	ctx := context.Background()

	// The agent cloned the delivery repo itself (git clone) and runs this
	// command from inside it, so the repo dir is the current working directory.
	// cs-cloud no longer manages checkout paths.
	gctx, err := readGiteaContext()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: submitting node_run=%s branch=%s\n", cfg.deliverableID, gctx.nodeRunID, gctx.nodeBranch)
	worktree, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	docPath, err := func() (string, error) {
		if cfg.deliverableID == "" {
			// Agent-defined: no pre-registered target path — use the local file's name.
			return filepath.Base(cfg.filePath), nil
		}
		return gctx.deliverablePath(cfg.deliverableID)
	}()
	if err != nil {
		return err
	}
	content, err := os.ReadFile(cfg.filePath)
	if err != nil {
		return fmt.Errorf("read --file: %w", err)
	}

	cred, err := readGiteaCredential()
	if err != nil {
		return fmt.Errorf("gitea credential: %w", err)
	}
	giteaBase := cfg.giteaBaseOverride
	if giteaBase == "" {
		giteaBase = cred.BaseURL
	}

	// The worktree is on the env-advertised node branch (CheckoutRepo put it
	// there). PR head = this branch; PR base = inst branch.
	currentBranch, err := cfg.gitOps.CurrentBranch(worktree)
	if err != nil {
		return fmt.Errorf("current branch: %w", err)
	}

	if err := cfg.gitOps.WriteFile(worktree, docPath, content); err != nil {
		return fmt.Errorf("write document: %w", err)
	}
	// Idempotent submit: if the document is byte-identical to what's already
	// committed (re-run), `git commit` exits 1 and would abort the pipeline
	// before push/PR/report. Skip commit on a clean tree so the command can be
	// retried to success.
	hasChanges, err := cfg.gitOps.HasChanges(worktree, docPath)
	if err != nil {
		return fmt.Errorf("detect changes: %w", err)
	}
	if hasChanges {
		commitLabel := cfg.deliverableID
		if commitLabel == "" {
			commitLabel = cfg.title
		}
		if err := cfg.gitOps.Commit(worktree, docPath, "deliverable: "+commitLabel); err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		fmt.Fprintf(os.Stderr, "deliverable %s: committed branch=%s\n", cfg.deliverableID, currentBranch)
	} else {
		fmt.Fprintf(os.Stderr, "deliverable %s: clean tree, nothing to commit — continuing\n", cfg.deliverableID)
	}

	// Validate the backend URL before committing to the push/open-PR flow — a
	// misconfigured/unset URL would otherwise orphan the Gitea PR we're about to
	// create (mirrors submitGitlabMR's existing guard).
	backendURL := envOr("CS_CLOUD_BACKEND_URL", "")
	if backendURL == "" {
		return fmt.Errorf("CS_CLOUD_BACKEND_URL not set")
	}
	deliverableID := cfg.deliverableID
	if deliverableID == "" {
		// Agent-defined: create the deliverable before pushing/opening the PR so
		// a create rejection cannot leave an external PR with no platform target.
		deliverableID, err = createAgentDefinedDeliverable(ctx, backendURL, os.Getenv("CS_CLOUD_TOKEN"), gctx.nodeRunID, cfg.title, os.Getenv("CS_CLOUD_WORKSPACE_ID"), os.Getenv("CS_CLOUD_AGENT_ID"), os.Getenv("CS_CLOUD_TASK_ID"))
		if err != nil {
			return fmt.Errorf("create deliverable: %w", err)
		}
		fmt.Fprintf(os.Stderr, "deliverable: created agent-defined id=%s title=%q\n", deliverableID, cfg.title)
	}

	// Push URL: prefer the server-provided full clone URL; fall back to
	// self-building from base + owner + repo.
	authURL := ""
	if gctx.cloneURL != "" {
		authURL = injectTokenIntoURL(gctx.cloneURL, cred.Token)
	}
	if authURL == "" {
		authURL = injectToken(giteaBase, gctx.owner, gctx.repo, cred.Token)
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: pushing branch=%s\n", cfg.deliverableID, currentBranch)
	if err := cfg.gitOps.Push(worktree, authURL, currentBranch); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	fmt.Fprintf(os.Stderr, "deliverable %s: opening PR head=%s base=%s\n", cfg.deliverableID, currentBranch, gctx.instBranch)
	prURL, err := openGiteaPR(ctx, giteaBase, cred.Token, gctx.owner, gctx.repo, currentBranch, gctx.instBranch, deliverableTitle(cfg.title, "document deliverable "+cfg.deliverableID))
	if err != nil {
		return fmt.Errorf("open PR: %w", err)
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: reporting PR\n", deliverableID)
	if err := reportDeliverablePR(ctx, backendURL, os.Getenv("CS_CLOUD_TOKEN"), gctx.nodeRunID, deliverableID, prURL, os.Getenv("CS_CLOUD_WORKSPACE_ID"), os.Getenv("CS_CLOUD_AGENT_ID"), os.Getenv("CS_CLOUD_TASK_ID")); err != nil {
		return fmt.Errorf("report PR: %w", err)
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: submitted pr=%s\n", deliverableID, prURL)
	fmt.Println(prURL)
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func deliverableTitle(title, fallback string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	return fallback
}

// tokenUsername returns the git auth username for embedding a PAT into a
// clone URL. GitHub (SaaS and Enterprise) requires "x-access-token" for
// fine-grained PAT compatibility; GitLab and Gitea accept "oauth2".
func tokenUsername(host string) string {
	h := strings.ToLower(strings.TrimSpace(host))
	if splitHost, _, err := net.SplitHostPort(h); err == nil {
		h = splitHost
	}
	if h == "github.com" || strings.HasSuffix(h, ".github.com") {
		return "x-access-token"
	}
	return "oauth2"
}

// injectToken builds an HTTPS clone URL with the PAT embedded for git auth.
func injectToken(baseURL, owner, repo, token string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = url.UserPassword(tokenUsername(u.Host), token)
	u.Path = fmt.Sprintf("/%s/%s.git", owner, repo)
	return u.String()
}

// injectTokenIntoURL injects the PAT into a full clone URL (already carrying
// owner/repo). Returns "" on an unparseable URL.
func injectTokenIntoURL(cloneURL, token string) string {
	return injectTokenIntoURLWithUsername(cloneURL, token, "")
}

func injectGithubTokenIntoURL(cloneURL, token string) string {
	return injectTokenIntoURLWithUsername(cloneURL, token, "x-access-token")
}

func injectTokenIntoURLWithUsername(cloneURL, token, username string) string {
	u, err := url.Parse(strings.TrimSpace(cloneURL))
	if err != nil || u.Host == "" {
		return ""
	}
	if strings.TrimSpace(username) == "" {
		username = tokenUsername(u.Host)
	}
	u.User = url.UserPassword(username, token)
	return u.String()
}

// sharedHTTPClient has a bounded timeout so a hung Gitea or the server endpoint
// cannot stall the agent CLI indefinitely.
var sharedHTTPClient = &http.Client{Timeout: 30 * time.Second}

// urlCredRedactor matches scheme://user:pass@host so git stderr never leaks PAT.
var urlCredRedactor = regexp.MustCompile(`(\w+://[^/:@]+:)[^@]+(@)`)

// readGiteaCredential reads the workspace bot PAT + base URL that the server
// pushed in the task payload env (CS_CLOUD_GITEA_*). cs-cloud talks to Gitea
// directly with these — there is no relay back through the server to fetch
// credentials, so the agent CLI never depends on CS_CLOUD_TOKEN for Gitea auth.
func readGiteaCredential() (struct {
	BaseURL string
	Token   string
}, error) {
	token := strings.TrimSpace(os.Getenv("CS_CLOUD_GITEA_TOKEN"))
	if token == "" {
		return struct {
			BaseURL string
			Token   string
		}{}, fmt.Errorf("CS_CLOUD_GITEA_TOKEN not set (the task payload must provide the workspace bot PAT)")
	}
	return struct {
		BaseURL string
		Token   string
	}{BaseURL: strings.TrimSpace(os.Getenv("CS_CLOUD_GITEA_BASE_URL")), Token: token}, nil
}

// normalizeGiteaBase returns base as a bare Gitea server root, suitable for
// appending "/api/v1/repos/{owner}/{repo}/pulls". Operators (or the workspace
// gitea_web_url setting) sometimes supply the repository web URL instead of
// the server root; openGiteaPR would otherwise build
// ".../{owner}/{repo}/api/v1/repos/{owner}/{repo}/pulls" and Gitea 404s.
func normalizeGiteaBase(base, owner, repo string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	// Strip a trailing "/{owner}/{repo}" or "/{owner}/{repo}.git" if present —
	// base is meant to be the server root, not the repository web URL. Matching
	// on the full owner+repo suffix avoids stripping unrelated path segments.
	for _, suffix := range []string{
		"/" + owner + "/" + repo + ".git",
		"/" + owner + "/" + repo,
	} {
		if strings.HasSuffix(b, suffix) {
			return strings.TrimSuffix(b, suffix)
		}
	}
	return b
}

// openGiteaPR POSTs /api/v1/repos/{owner}/{repo}/pulls and returns html_url.
func openGiteaPR(ctx context.Context, base, token, owner, repo, head, baseBranch, title string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"head":  head,
		"base":  baseBranch,
		"title": deliverableTitle(title, ""),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		normalizeGiteaBase(base, owner, repo)+"/api/v1/repos/"+owner+"/"+repo+"/pulls", bytes.NewReader(body))
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
	if resp.StatusCode == http.StatusConflict {
		// PR already exists (re-run after a prior success, or a manually created
		// PR). Look up the existing open PR by head and return its html_url so
		// reporting can proceed — submit stays idempotent.
		return findExistingGiteaPR(ctx, base, token, owner, repo, head)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gitea create PR: status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	var pr struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	if err := json.Unmarshal(respBody, &pr); err != nil {
		return "", fmt.Errorf("parse PR response: %w", err)
	}
	return pr.HTMLURL, nil
}

// findExistingGiteaPR lists a repo's open PRs and returns the html_url of the
// one whose head ref matches. Used when openGiteaPR's POST returned 409 (the PR
// was already opened, e.g. on a re-run) so submission is idempotent.
func findExistingGiteaPR(ctx context.Context, base, token, owner, repo, head string) (string, error) {
	listURL := strings.TrimRight(normalizeGiteaBase(base, owner, repo), "/") +
		"/api/v1/repos/" + owner + "/" + repo + "/pulls?state=open"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return "", fmt.Errorf("build list existing PRs request: %w", err)
	}
	req.Header.Set("Authorization", "token "+token)
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("list existing PRs: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("list existing PRs: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var prs []struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
		Head    struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := json.Unmarshal(body, &prs); err != nil {
		return "", fmt.Errorf("parse existing PR list: %w", err)
	}
	for _, pr := range prs {
		if pr.Head.Ref == head && pr.HTMLURL != "" {
			return pr.HTMLURL, nil
		}
	}
	return "", fmt.Errorf("PR already exists (409) but no open PR with head %q found", head)
}

// reportToServer POSTs a pull_request_url to the given server endpoint.
func reportToServer(ctx context.Context, serverURL, token, endpoint, prURL, workspaceID, agentID, taskID string) error {
	body, _ := json.Marshal(map[string]string{"pull_request_url": prURL})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build report request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	// multica's RequireWorkspaceMember middleware gates this endpoint on a
	// workspace identifier; without X-Workspace-ID the report is rejected with
	// 400 "workspace_id or workspace_slug is required" AFTER the PR/MR is
	// already opened, orphaning it. CS_CLOUD_WORKSPACE_ID is always present in
	// the task env, so forward it as the header the middleware reads.
	if workspaceID != "" {
		req.Header.Set("X-Workspace-ID", workspaceID)
	}
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}
	if taskID != "" {
		req.Header.Set("X-Task-ID", taskID)
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("report request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("report: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// reportDeliverablePR POSTs the PR URL to the unified submit endpoint
// (same endpoint the code-MR path uses).
func reportDeliverablePR(ctx context.Context, serverURL, token, nodeRunID, deliverableID, prURL, workspaceID, agentID, taskID string) error {
	return reportToServer(ctx, serverURL, token,
		serverURL+"/api/node-runs/"+nodeRunID+"/deliverables/"+deliverableID+"/submit", prURL, workspaceID, agentID, taskID)
}

// createAgentDefinedDeliverable creates a run-scoped deliverable the agent
// defined itself (no pre-registered node deliverable) and returns the new id.
// Used by the document submit flow when --deliverable is omitted: the CLI
// creates the deliverable then reports the PR against it, so the agent still
// runs a single command.
func createAgentDefinedDeliverable(ctx context.Context, serverURL, token, nodeRunID, title, workspaceID, agentID, taskID string) (string, error) {
	body, _ := json.Marshal(map[string]string{"title": title})
	endpoint := serverURL + "/api/node-runs/" + nodeRunID + "/deliverables"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build create deliverable request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	if key := agentDefinedDeliverableIdempotencyKey(nodeRunID, title, taskID); key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	if workspaceID != "" {
		req.Header.Set("X-Workspace-ID", workspaceID)
	}
	if agentID != "" {
		req.Header.Set("X-Agent-ID", agentID)
	}
	if taskID != "" {
		req.Header.Set("X-Task-ID", taskID)
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create deliverable request: %w", err)
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("create deliverable: status %d: %s", resp.StatusCode, strings.TrimSpace(string(rb)))
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rb, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("create deliverable: parse id: %s", strings.TrimSpace(string(rb)))
	}
	return out.ID, nil
}

func agentDefinedDeliverableIdempotencyKey(nodeRunID, title, taskID string) string {
	parts := []string{"agent-defined-deliverable", strings.TrimSpace(nodeRunID), strings.TrimSpace(taskID), strings.TrimSpace(title)}
	return strings.Join(parts, ":")
}

// execGitOps implements gitOps via shelled-out git.
type execGitOps struct{}

func (execGitOps) Clone(authURL, branch, dir string) error {
	return runGitInDir("", "clone", "--depth", "1", "--single-branch", "--branch", branch, authURL, dir)
}
func (execGitOps) WriteFile(dir, path string, content []byte) error {
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}
func (execGitOps) Commit(dir, path, message string) error {
	// Stage ONLY the deliverable path, not `add -A`. The worktree may hold
	// unrelated files — a leaked .cs-cloud.env carrying tokens, scratch files,
	// build output — and force-pushing those (see Push) would leak secrets and
	// pollute the delivery PR.
	if err := runGitInDir(dir, "add", "--", path); err != nil {
		return err
	}
	return runGitInDir(dir, "-c", "user.email=bot@cs-cloud", "-c", "user.name=CS-Cloud Bot", "commit", "-m", message)
}
func (execGitOps) HasChanges(dir, path string) (bool, error) {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain", "--", path).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) != "", nil
}
func (execGitOps) Push(dir, authURL, branch string) error {
	// Force-push: a node branch has a single writer pre-merge; re-submit
	// replaces WIP and the open PR auto-updates.
	return runGitInDir(dir, "push", "--force", authURL, branch)
}
func (execGitOps) CurrentBranch(dir string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git rev-parse --abbrev-ref HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runGitInDir runs git in the given dir ("" = inherit cwd), streaming stderr
// with any URL-embedded credentials redacted (clone/push auth URL has the PAT).
func runGitInDir(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stdout = os.Stderr
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Stderr.Write([]byte(urlCredRedactor.ReplaceAllString(stderr.String(), "${1}***${2}")))
		return err
	}
	if stderr.Len() > 0 {
		os.Stderr.Write([]byte(urlCredRedactor.ReplaceAllString(stderr.String(), "${1}***${2}")))
	}
	return nil
}
