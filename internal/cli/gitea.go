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
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cs-cloud/internal/app"
)

// giteaCmd implements `cs-cloud gitea <subcommand>`. It is the cs-cloud home
// for the platform-Gitea document-deliverable operations migrated from
// multica's cs-workflow CLI. Subcommands run inside a workflow-node task
// context: they read MULTICA_GITEA_* / MULTICA_TOKEN / MULTICA_SERVER_URL env
// (pushed by multica in the task payload) and are invoked by the agent (csc)
// during a node run.
func giteaCmd(a *app.App, args []string) error {
	_ = a // task-context command; uses task env, not daemon config/credentials
	if len(args) == 0 {
		printGiteaUsage()
		return nil
	}
	switch args[0] {
	case "submit":
		return runGiteaSubmit(args[1:])
	case "fetch":
		return runGiteaFetch(args[1:])
	case "help", "-h", "--help":
		printGiteaUsage()
		return nil
	default:
		printGiteaUsage()
		return fmt.Errorf("unknown gitea command: %s", args[0])
	}
}

func printGiteaUsage() {
	fmt.Println(`gitea - platform git-server deliverable operations

Usage:
  cs-cloud gitea submit --deliverable <id> --file <path>
    Push a document deliverable to the platform Gitea and open a PR.
    Reads MULTICA_GITEA_* env (set by the task payload), fetches the workspace
    Gitea PAT, pushes the document to the node branch, opens a Gitea PR
    (node->inst), and registers the PR URL back to Multica.

  cs-cloud gitea fetch <node-run-id> [--deliverable <id>]
    Read a node-run's document deliverable content. Fetches the node-run's Gitea
    context from Multica, clones the run's inst branch with the workspace PAT,
    and prints the deliverable body (all document deliverables, or just one with
    --deliverable). Works for ANY node-run in the workspace the token can reach,
    not just the caller's own task.`)
}

// runGiteaFetch parses args and runs the fetch flow.
func runGiteaFetch(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: cs-cloud gitea fetch <node-run-id> [--deliverable <id>]")
	}
	nodeRunID := args[0]
	var wantID string
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--deliverable" {
			if i+1 >= len(rest) {
				return fmt.Errorf("--deliverable needs a value")
			}
			wantID = rest[i+1]
			i++
		} else {
			return fmt.Errorf("unknown argument: %s", rest[i])
		}
	}
	return fetchDeliverables(fetchConfig{
		nodeRunID: nodeRunID,
		wantID:    wantID,
		cloner:    execFetchCloner{},
	})
}

// fetchConfig parameterizes fetchDeliverables for testing.
type fetchConfig struct {
	nodeRunID         string
	wantID            string // optional: only this deliverable
	cloner            fetchCloner
	out               io.Writer // optional: output sink (defaults to os.Stdout)
	giteaBaseOverride string    // test-only
}

func (c fetchConfig) writer() io.Writer {
	if c.out != nil {
		return c.out
	}
	return os.Stdout
}

// fetchCloner abstracts the clone + read so the fetch flow is unit-testable.
type fetchCloner interface {
	Clone(authURL, branch, dir string) error
	ReadFile(dir, path string) ([]byte, error)
}

// nodeRunGiteaContext mirrors multica's GiteaDeliverableContext JSON.
type nodeRunGiteaContext struct {
	Owner        string                `json:"owner"`
	Repo         string                `json:"repo"`
	CloneURL     string                `json:"clone_url"`
	InstBranch   string                `json:"inst_branch"`
	NodeBranch   string                `json:"node_branch"`
	Deliverables []giteaDeliverableRef `json:"deliverables"`
}

// fetchDeliverables is the testable core: get the node-run's Gitea context,
// clone the inst branch with the PAT, and print each document deliverable body.
func fetchDeliverables(cfg fetchConfig) error {
	serverURL := envOr("MULTICA_SERVER_URL", "")
	token := os.Getenv("MULTICA_TOKEN")
	workspaceID := os.Getenv("MULTICA_WORKSPACE_ID")

	ctx, err := fetchGiteaNodeRunContext(serverURL, token, workspaceID, cfg.nodeRunID)
	if err != nil {
		return fmt.Errorf("fetch gitea context: %w", err)
	}
	if len(ctx.Deliverables) == 0 {
		return fmt.Errorf("node run %s has no document deliverables", cfg.nodeRunID)
	}
	cred, err := fetchGiteaCredential(serverURL, token, workspaceID)
	if err != nil {
		return fmt.Errorf("fetch gitea credential: %w", err)
	}
	cloneAuth := injectTokenIntoURL(ctx.CloneURL, cred.Token)
	if cloneAuth == "" {
		giteaBase := cfg.giteaBaseOverride
		if giteaBase == "" {
			giteaBase = cred.BaseURL
		}
		cloneAuth = injectToken(giteaBase, ctx.Owner, ctx.Repo, cred.Token)
	}

	dir, err := os.MkdirTemp("", "cscloud-fetch-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// Clone the run's inst branch — it carries every merged deliverable as a
	// file under nodes/<nodeRunShort>/<deliverableShort>.md.
	if err := cfg.cloner.Clone(cloneAuth, ctx.InstBranch, dir); err != nil {
		return fmt.Errorf("clone inst branch: %w", err)
	}

	printed := 0
	out := cfg.writer()
	for _, d := range ctx.Deliverables {
		if cfg.wantID != "" && d.ID != cfg.wantID {
			continue
		}
		content, err := cfg.cloner.ReadFile(dir, d.Path)
		if err != nil {
			return fmt.Errorf("read deliverable %s (%s): %w", d.ID, d.Path, err)
		}
		fmt.Fprintf(out, "=== %s (%s) ===\n%s\n", d.Title, d.ID, content)
		printed++
	}
	if printed == 0 {
		return fmt.Errorf("deliverable %q not found on node run %s", cfg.wantID, cfg.nodeRunID)
	}
	return nil
}

// fetchGiteaNodeRunContext calls GET /api/daemon/node-runs/{id}/gitea-context.
func fetchGiteaNodeRunContext(serverURL, token, workspaceID, nodeRunID string) (*nodeRunGiteaContext, error) {
	if serverURL == "" || token == "" {
		return nil, fmt.Errorf("MULTICA_SERVER_URL/MULTICA_TOKEN not set")
	}
	req, _ := http.NewRequest(http.MethodGet,
		serverURL+"/api/daemon/node-runs/"+nodeRunID+"/gitea-context", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if workspaceID != "" {
		req.Header.Set("X-Workspace-ID", workspaceID)
	}
	resp, err := giteaHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gitea-context request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("gitea-context: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out nodeRunGiteaContext
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// execFetchCloner is the production cloner: shallow git clone + filesystem read.
type execFetchCloner struct{}

func (execFetchCloner) Clone(authURL, branch, dir string) error {
	return runGitInDir("", "clone", "--depth", "1", "--single-branch", "--branch", branch, authURL, dir)
}
func (execFetchCloner) ReadFile(dir, path string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, path))
}

// runGiteaSubmit parses flags and runs the submit flow.
func runGiteaSubmit(args []string) error {
	deliverableID, filePath, err := parseSubmitArgs(args)
	if err != nil {
		return err
	}
	return submitDeliverable(submitConfig{
		deliverableID: deliverableID,
		filePath:      filePath,
		gitOps:        &execGitOps{},
	})
}

// parseSubmitArgs parses `--deliverable <id> --file <path>` from the flat arg
// slice (cs-cloud's dispatcher has no flag library, so we parse by hand).
func parseSubmitArgs(args []string) (deliverable, file string, err error) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--deliverable":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--deliverable needs a value")
			}
			deliverable = args[i+1]
			i += 2
		case "--file":
			if i+1 >= len(args) {
				return "", "", fmt.Errorf("--file needs a value")
			}
			file = args[i+1]
			i += 2
		default:
			return "", "", fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	if deliverable == "" {
		return "", "", fmt.Errorf("--deliverable is required")
	}
	if file == "" {
		return "", "", fmt.Errorf("--file is required")
	}
	return deliverable, file, nil
}

// submitConfig parameterizes submitDeliverable for testing.
type submitConfig struct {
	deliverableID     string
	filePath          string
	gitOps            gitOps
	giteaBaseOverride string // test-only: override the Gitea base URL (else from credential)
}

// gitOps abstracts the git operations so the submit flow is unit-testable.
// The production impl (execGitOps) shells out to git.
type gitOps interface {
	Clone(authURL, branch, dir string) error
	PrepareBranch(dir, nodeBranch string) error
	WriteFile(dir, path string, content []byte) error
	Commit(dir, message string) error
	Push(dir, authURL, branch string) error
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
		nodeRunID:  os.Getenv("MULTICA_NODE_RUN_ID"),
		owner:      os.Getenv("MULTICA_GITEA_OWNER"),
		repo:       os.Getenv("MULTICA_GITEA_REPO"),
		cloneURL:   os.Getenv("MULTICA_GITEA_CLONE_URL"),
		instBranch: os.Getenv("MULTICA_GITEA_INST_BRANCH"),
		nodeBranch: os.Getenv("MULTICA_GITEA_NODE_BRANCH"),
	}
	if c.nodeRunID == "" {
		return nil, fmt.Errorf("MULTICA_NODE_RUN_ID not set; this command must run inside a workflow-node task")
	}
	for _, f := range []string{c.owner, c.repo, c.instBranch, c.nodeBranch} {
		if f == "" {
			return nil, fmt.Errorf("MULTICA_GITEA_* env incomplete (owner/repo/inst/node-branch required)")
		}
	}
	raw := os.Getenv("MULTICA_GITEA_DELIVERABLES")
	if raw == "" {
		return nil, fmt.Errorf("MULTICA_GITEA_DELIVERABLES not set")
	}
	if err := json.Unmarshal([]byte(raw), &c.deliverables); err != nil {
		return nil, fmt.Errorf("parse MULTICA_GITEA_DELIVERABLES: %w", err)
	}
	return c, nil
}

func (c *giteaContext) deliverablePath(id string) (string, error) {
	for _, d := range c.deliverables {
		if d.ID == id {
			return d.Path, nil
		}
	}
	return "", fmt.Errorf("deliverable %q not in MULTICA_GITEA_DELIVERABLES", id)
}

// submitDeliverable is the testable core. Returns nil only after the PR is
// registered back to Multica.
func submitDeliverable(cfg submitConfig) error {
	ctx := context.Background()

	gctx, err := readGiteaContext()
	if err != nil {
		return err
	}
	docPath, err := gctx.deliverablePath(cfg.deliverableID)
	if err != nil {
		return err
	}
	content, err := os.ReadFile(cfg.filePath)
	if err != nil {
		return fmt.Errorf("read --file: %w", err)
	}

	cred, err := fetchGiteaCredential(envOr("MULTICA_SERVER_URL", ""), os.Getenv("MULTICA_TOKEN"), os.Getenv("MULTICA_WORKSPACE_ID"))
	if err != nil {
		return fmt.Errorf("fetch gitea credential: %w", err)
	}
	giteaBase := cfg.giteaBaseOverride
	if giteaBase == "" {
		giteaBase = cred.BaseURL
	}
	// Prefer the server-provided full clone URL; fall back to self-building.
	cloneAuth := ""
	if gctx.cloneURL != "" {
		cloneAuth = injectTokenIntoURL(gctx.cloneURL, cred.Token)
	}
	if cloneAuth == "" {
		cloneAuth = injectToken(giteaBase, gctx.owner, gctx.repo, cred.Token)
	}

	dir, err := os.MkdirTemp("", "cscloud-gitea-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	if err := cfg.gitOps.Clone(cloneAuth, gctx.instBranch, dir); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	if err := cfg.gitOps.PrepareBranch(dir, gctx.nodeBranch); err != nil {
		return fmt.Errorf("prepare node branch: %w", err)
	}
	if err := cfg.gitOps.WriteFile(dir, docPath, content); err != nil {
		return fmt.Errorf("write document: %w", err)
	}
	if err := cfg.gitOps.Commit(dir, "deliverable: "+cfg.deliverableID); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	if err := cfg.gitOps.Push(dir, cloneAuth, gctx.nodeBranch); err != nil {
		return fmt.Errorf("push: %w", err)
	}

	prURL, err := openGiteaPR(ctx, giteaBase, cred.Token, gctx.owner, gctx.repo, gctx.nodeBranch, gctx.instBranch, cfg.deliverableID)
	if err != nil {
		return fmt.Errorf("open PR: %w", err)
	}
	if err := reportDeliverablePR(ctx, envOr("MULTICA_SERVER_URL", ""), os.Getenv("MULTICA_TOKEN"), gctx.nodeRunID, cfg.deliverableID, prURL); err != nil {
		return fmt.Errorf("report PR: %w", err)
	}
	fmt.Println(prURL)
	return nil
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// injectToken builds an HTTPS clone URL with the PAT embedded for git auth.
func injectToken(baseURL, owner, repo, token string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = url.UserPassword("oauth2", token)
	u.Path = fmt.Sprintf("/%s/%s.git", owner, repo)
	return u.String()
}

// injectTokenIntoURL injects the PAT into a full clone URL (already carrying
// owner/repo). Returns "" on an unparseable URL.
func injectTokenIntoURL(cloneURL, token string) string {
	u, err := url.Parse(strings.TrimSpace(cloneURL))
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = url.UserPassword("oauth2", token)
	return u.String()
}

// giteaHTTPClient has a bounded timeout so a hung Gitea or Multica endpoint
// cannot stall the agent CLI indefinitely.
var giteaHTTPClient = &http.Client{Timeout: 30 * time.Second}

// urlCredRedactor matches scheme://user:pass@host so git stderr never leaks PAT.
var urlCredRedactor = regexp.MustCompile(`(\w+://[^/:@]+:)[^@]+(@)`)

// fetchGiteaCredential calls GET /api/gitea/credential on the multica server.
func fetchGiteaCredential(serverURL, token, workspaceID string) (struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}, error) {
	var out struct {
		BaseURL string `json:"base_url"`
		Token   string `json:"token"`
	}
	if serverURL == "" || token == "" {
		return out, fmt.Errorf("MULTICA_SERVER_URL/MULTICA_TOKEN not set")
	}
	req, _ := http.NewRequest(http.MethodGet, serverURL+"/api/gitea/credential", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if workspaceID != "" {
		req.Header.Set("X-Workspace-ID", workspaceID)
	}
	resp, err := giteaHTTPClient.Do(req)
	if err != nil {
		return out, fmt.Errorf("credential request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return out, fmt.Errorf("credential: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return out, err
	}
	if out.BaseURL == "" || out.Token == "" {
		return out, fmt.Errorf("credential response missing base_url/token")
	}
	return out, nil
}

// openGiteaPR POSTs /api/v1/repos/{owner}/{repo}/pulls and returns html_url.
func openGiteaPR(ctx context.Context, base, token, owner, repo, head, baseBranch, deliverableID string) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"head":  head,
		"base":  baseBranch,
		"title": "document deliverable " + deliverableID,
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/api/v1/repos/"+owner+"/"+repo+"/pulls", bytes.NewReader(body))
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := giteaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("create PR request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
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

// reportDeliverablePR POSTs the PR URL to the multica daemon report-pr endpoint.
func reportDeliverablePR(ctx context.Context, serverURL, token, nodeRunID, deliverableID, prURL string) error {
	body, _ := json.Marshal(map[string]string{"pull_request_url": prURL})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		serverURL+"/api/daemon/node-runs/"+nodeRunID+"/deliverables/"+deliverableID+"/report-pr", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := giteaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("report-pr request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("report-pr: status %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// execGitOps implements gitOps via shelled-out git.
type execGitOps struct{}

func (execGitOps) Clone(authURL, branch, dir string) error {
	return runGitInDir("", "clone", "--depth", "1", "--single-branch", "--branch", branch, authURL, dir)
}
func (execGitOps) PrepareBranch(dir, nodeBranch string) error {
	// -B resets the branch if it exists (idempotent re-submit from fresh clone).
	return runGitInDir(dir, "checkout", "-B", nodeBranch, "HEAD")
}
func (execGitOps) WriteFile(dir, path string, content []byte) error {
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, 0o644)
}
func (execGitOps) Commit(dir, message string) error {
	if err := runGitInDir(dir, "add", "-A"); err != nil {
		return err
	}
	return runGitInDir(dir, "-c", "user.email=bot@multica", "-c", "user.name=Multica Bot", "commit", "-m", message)
}
func (execGitOps) Push(dir, authURL, branch string) error {
	// Force-push: a node branch has a single writer pre-merge; re-submit
	// replaces WIP and the open PR auto-updates.
	return runGitInDir(dir, "push", "--force", authURL, branch)
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
