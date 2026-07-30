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
	fmt.Println(`deliverable - document deliverable operations

Usage:
  cs-cloud workflow deliverable submit --deliverable <id> --file <path>
    Push a document deliverable to the platform Gitea and open a PR.
    Reads CS_CLOUD_GITEA_* env (set by the task payload), fetches the workspace
    Gitea PAT, pushes the document to the node branch, opens a Gitea PR
    (node->inst), and registers the PR URL back to the server.`)
}

// runGiteaSubmit parses flags and runs the submit flow.
func runGiteaSubmit(args []string) error {
	deliverableID, filePath, mrMode, repoURL, err := parseSubmitArgs(args)
	if err != nil {
		return err
	}
	return submitDeliverable(submitConfig{
		deliverableID: deliverableID,
		filePath:      filePath,
		mrMode:        mrMode,
		repoURL:       repoURL,
		gitOps:        &execGitOps{},
	})
}

// parseSubmitArgs parses `--deliverable <id> --file <path> [--mr --repo <url>]` from the flat arg
// slice (cs-cloud's dispatcher has no flag library, so we parse by hand).
func parseSubmitArgs(args []string) (deliverable, file string, mrMode bool, repoURL string, err error) {
	i := 0
	for i < len(args) {
		switch args[i] {
		case "--deliverable":
			if i+1 >= len(args) {
				return "", "", false, "", fmt.Errorf("--deliverable needs a value")
			}
			deliverable = args[i+1]
			i += 2
		case "--file":
			if i+1 >= len(args) {
				return "", "", false, "", fmt.Errorf("--file needs a value")
			}
			file = args[i+1]
			i += 2
		case "--mr":
			mrMode = true
			i++
		case "--repo":
			if i+1 >= len(args) {
				return "", "", false, "", fmt.Errorf("--repo needs a value")
			}
			repoURL = args[i+1]
			i += 2
		default:
			return "", "", false, "", fmt.Errorf("unknown argument: %s", args[i])
		}
	}
	if deliverable == "" {
		return "", "", false, "", fmt.Errorf("--deliverable is required")
	}
	if mrMode {
		// --mr mode does not require --file (agent already edited in worktree).
		if repoURL == "" {
			return "", "", false, "", fmt.Errorf("--repo is required with --mr")
		}
	} else {
		if file == "" {
			return "", "", false, "", fmt.Errorf("--file is required")
		}
	}
	return deliverable, file, mrMode, repoURL, nil
}

// submitConfig parameterizes submitDeliverable for testing.
type submitConfig struct {
	deliverableID     string
	filePath          string
	gitOps            gitOps
	giteaBaseOverride string // test-only: override the Gitea base URL (else from credential)
	mrMode            bool   // --mr: code MR mode
	repoURL           string // --mr mode: code repository URL (agent worktree already checked out)
}

// gitOps abstracts the git operations so the submit flow is unit-testable.
// The production impl (execGitOps) shells out to git.
type gitOps interface {
	Clone(authURL, branch, dir string) error
	WriteFile(dir, path string, content []byte) error
	Commit(dir, message string) error
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
		return nil, fmt.Errorf("CS_CLOUD_GITEA_DELIVERABLES not set")
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
	if cfg.mrMode {
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

	docPath, err := gctx.deliverablePath(cfg.deliverableID)
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
	if err := cfg.gitOps.Commit(worktree, "deliverable: "+cfg.deliverableID); err != nil {
		return fmt.Errorf("commit: %w", err)
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
	prURL, err := openGiteaPR(ctx, giteaBase, cred.Token, gctx.owner, gctx.repo, currentBranch, gctx.instBranch, cfg.deliverableID)
	if err != nil {
		return fmt.Errorf("open PR: %w", err)
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: reporting PR\n", cfg.deliverableID)
	if err := reportDeliverablePR(ctx, envOr("CS_CLOUD_BACKEND_URL", ""), os.Getenv("CS_CLOUD_TOKEN"), gctx.nodeRunID, cfg.deliverableID, prURL); err != nil {
		return fmt.Errorf("report PR: %w", err)
	}
	fmt.Fprintf(os.Stderr, "deliverable %s: submitted pr=%s\n", cfg.deliverableID, prURL)
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
	resp, err := sharedHTTPClient.Do(req)
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

// reportToServer POSTs a pull_request_url to the given server endpoint.
func reportToServer(ctx context.Context, serverURL, token, endpoint, prURL string) error {
	body, _ := json.Marshal(map[string]string{"pull_request_url": prURL})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
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
func reportDeliverablePR(ctx context.Context, serverURL, token, nodeRunID, deliverableID, prURL string) error {
	return reportToServer(ctx, serverURL, token,
		serverURL+"/api/node-runs/"+nodeRunID+"/deliverables/"+deliverableID+"/submit", prURL)
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
func (execGitOps) Commit(dir, message string) error {
	if err := runGitInDir(dir, "add", "-A"); err != nil {
		return err
	}
	return runGitInDir(dir, "-c", "user.email=bot@cs-cloud", "-c", "user.name=CS-Cloud Bot", "commit", "-m", message)
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
