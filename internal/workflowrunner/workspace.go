package workflowrunner

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const gitTimeout = 5 * time.Minute

// WorkspaceManager manages workspace directories and repo caches.
type WorkspaceManager struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewWorkspaceManager creates a new WorkspaceManager rooted at root.
func NewWorkspaceManager(root string) *WorkspaceManager {
	return &WorkspaceManager{root: root, locks: make(map[string]*sync.Mutex)}
}

// validateID rejects IDs that could escape wm.root via path traversal. IDs must
// be a single safe path component: no separators, no "."/"..".
func validateID(id string) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("invalid id %q", id)
	}
	if strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid id %q", id)
	}
	if strings.Contains(filepath.Clean(id), string(filepath.Separator)) {
		return fmt.Errorf("invalid id %q", id)
	}
	return nil
}

// lockFor returns a per-path mutex used to serialize repo-cache provisioning so
// concurrent tasks for the same repo do not race through RemoveAll + clone.
func (wm *WorkspaceManager) lockFor(path string) *sync.Mutex {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	lock, ok := wm.locks[path]
	if !ok {
		lock = &sync.Mutex{}
		wm.locks[path] = lock
	}
	return lock
}

// EnsureRoot creates the root directory if it does not exist.
func (wm *WorkspaceManager) EnsureRoot() error {
	return os.MkdirAll(wm.root, 0o755)
}

// WorkspaceDir returns the directory for a workspace.
func (wm *WorkspaceManager) WorkspaceDir(workspaceID string) string {
	return filepath.Join(wm.root, workspaceID)
}

// RepoCacheDir returns the directory where repo mirrors are cached.
func (wm *WorkspaceManager) RepoCacheDir(workspaceID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "repos")
}

// TaskWorktreeDir returns the worktree directory for a task.
func (wm *WorkspaceManager) TaskWorktreeDir(workspaceID, taskID string) string {
	return filepath.Join(wm.WorkspaceDir(workspaceID), "tasks", taskID)
}

// EnsureRepoReady ensures a mirror clone of repoURL exists for the workspace.
// On first use it clones; on subsequent calls it updates the existing mirror.
// accessToken (optional) is embedded as HTTP basic auth for private GitLab
// repos; empty token or a non-host URL (e.g. local path) leaves repoURL as-is.
func (wm *WorkspaceManager) EnsureRepoReady(workspaceID, repoURL, accessToken string) (string, error) {
	if repoURL == "" {
		return "", nil
	}
	if err := validateID(workspaceID); err != nil {
		return "", err
	}
	cacheDir := wm.RepoCacheDir(workspaceID)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}

	name := repoName(repoURL)
	if err := validateID(name); err != nil {
		return "", fmt.Errorf("invalid repo url %q: %w", repoURL, err)
	}
	cache := filepath.Join(cacheDir, name)

	// Serialize provisioning per cache path: concurrent tasks for one repo can
	// both observe a missing HEAD, then race through RemoveAll and git clone.
	cacheLock := wm.lockFor(cache)
	cacheLock.Lock()
	defer cacheLock.Unlock()

	authedURL := injectToken(repoURL, accessToken)
	head := filepath.Join(cache, "HEAD")
	if _, err := os.Stat(head); err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// Remove any incomplete cache left by an interrupted clone, then clone.
		_ = os.RemoveAll(cache)
		if err := runGit("clone", "--mirror", authedURL, cache); err != nil {
			return "", fmt.Errorf("clone repo: %w", err)
		}
	} else {
		if err := runGit("-C", cache, "remote", "update"); err != nil {
			return "", fmt.Errorf("update repo: %w", err)
		}
	}
	return cache, nil
}

// CreateWorktree ensures the repo cache is ready and creates a git worktree
// for the task at the given ref. If repoURL is empty, it simply creates the
// task directory without a git worktree.
func (wm *WorkspaceManager) CreateWorktree(workspaceID, taskID, repoURL, ref string) (string, error) {
	if err := validateID(workspaceID); err != nil {
		return "", err
	}
	if err := validateID(taskID); err != nil {
		return "", err
	}
	dir := wm.TaskWorktreeDir(workspaceID, taskID)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}

	if repoURL == "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		return dir, nil
	}

	cache, err := wm.EnsureRepoReady(workspaceID, repoURL, "")
	if err != nil {
		return "", err
	}

	// If the worktree exists but was created for a different ref, remove it
	// so the worktree matches the requested ref.
	if existingRef, _ := readWorktreeRef(dir); existingRef != "" && existingRef != ref {
		_ = runGit("-C", cache, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
	}

	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	if err := runGit("-C", cache, "worktree", "add", dir, ref); err != nil {
		return "", fmt.Errorf("add worktree: %w", err)
	}
	if err := writeWorktreeRef(dir, ref); err != nil {
		return "", fmt.Errorf("write worktree ref marker: %w", err)
	}
	return dir, nil
}

// repoName extracts the repository name from a URL.
func repoName(url string) string {
	base := filepath.Base(url)
	if ext := filepath.Ext(base); ext == ".git" {
		return base[:len(base)-len(".git")]
	}
	return base
}

// runGit runs a git command with a default timeout and returns a wrapped error
// on failure.
func runGit(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	return runGitCtx(ctx, args...)
}

// runGitCtx runs a git command under the provided context.
func runGitCtx(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %w: %s", args, err, out)
	}
	return nil
}

// runGitOutput runs a git command with the default git timeout and returns its
// stdout. Mirrors runGit but captures output for callers that need it (e.g.
// base-ref discovery).
func runGitOutput(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	return cmd.Output()
}

func worktreeRefPath(dir string) string {
	return filepath.Join(dir, ".cs-workflow-ref")
}

func readWorktreeRef(dir string) (string, error) {
	b, err := os.ReadFile(worktreeRefPath(dir))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func writeWorktreeRef(dir, ref string) error {
	return os.WriteFile(worktreeRefPath(dir), []byte(ref+"\n"), 0o644)
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// sanitizeName lowercases and collapses non-alphanumerics to '-', capping
// length. Empty/non-ascii input falls back to "agent". Mirrors multica repocache
// sanitizeName (cache.go:968).
func sanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "agent"
	}
	s = nonAlnum.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "agent"
	}
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}

// shortID returns the first 8 hex chars of a UUID (dashes stripped), mirroring
// multica repocache shortID (cache.go:983).
func shortID(id string) string {
	r := strings.ReplaceAll(id, "-", "")
	if len(r) > 8 {
		r = r[:8]
	}
	return r
}

// agentBranch builds the per-task working branch for a code repo:
// agent/<sanitize(agent)>/<shortTaskID>. Mirrors multica cache.go:449.
func agentBranch(agentName, taskID string) string {
	return fmt.Sprintf("agent/%s/%s", sanitizeName(agentName), shortID(taskID))
}

// injectToken embeds an access token as HTTP basic auth (oauth2:<token>) into a
// git URL so `git clone`/`fetch` can authenticate to a private GitLab. Empty
// token, a non-host URL (e.g. local path), or a non-http(s) scheme (e.g. ssh://,
// git://, file://) leaves the URL untouched: token basic-auth is meaningless
// over those transports and injecting it would corrupt the URL. Used for the
// local daemon's mirror clone; the token is visible in the git process args
// on this host.
func injectToken(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return rawURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return rawURL // ssh/git/file URLs: token basic-auth is meaningless, leave untouched
	}
	u.User = url.UserPassword("oauth2", token)
	return u.String()
}

// ResetWorktree resets an existing worktree to a clean base and checks out a new
// branch off baseRef, discarding uncommitted changes (committed/pushed work is
// in the remote, not lost). Used when resuming a prior workdir for a new round.
func (wm *WorkspaceManager) ResetWorktree(workDir, branchName, baseRef string) error {
	if err := runGit("-C", workDir, "reset", "--hard"); err != nil {
		return fmt.Errorf("reset: %w", err)
	}
	if err := runGit("-C", workDir, "clean", "-fd", "-e", ".cs-workflow-ref"); err != nil {
		return fmt.Errorf("clean: %w", err)
	}
	if err := runGit("-C", workDir, "checkout", "-b", branchName, baseRef); err != nil {
		if isBranchCollision(err) {
			retry := fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
			if err2 := runGit("-C", workDir, "checkout", "-b", retry, baseRef); err2 == nil {
				return nil
			}
		}
		return fmt.Errorf("checkout -b: %w", err)
	}
	return nil
}

// isBranchCollision reports whether err is git's "a branch named ... already
// exists" collision. Mirrors multica isBranchCollisionError (cache.go:599).
func isBranchCollision(err error) bool {
	return err != nil && strings.Contains(err.Error(), "a branch named")
}

// RepoWorktreeDir returns the per-repo worktree path under a task root:
// <taskRoot>/<repoName>. One task may hold several repo worktrees.
func RepoWorktreeDir(taskRoot, repoURL string) string {
	return filepath.Join(taskRoot, repoName(repoURL))
}

// resolveBaseRef resolves the base ref for a new worktree: the given baseBranch
// if non-empty, else the remote default branch discovered from the mirror cache.
func (wm *WorkspaceManager) resolveBaseRef(cache, baseBranch string) (string, error) {
	if baseBranch != "" {
		return baseBranch, nil
	}
	out, err := runGitOutput("-C", cache, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		if ref := strings.TrimSpace(string(out)); ref != "" && ref != "HEAD" {
			return ref, nil
		}
	}
	out, err = runGitOutput("-C", cache, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return "", fmt.Errorf("resolve base ref: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if ref := strings.TrimSpace(line); ref != "" {
			return ref, nil
		}
	}
	return "", fmt.Errorf("no base ref in %s", cache)
}

// CheckoutRepo ensures the mirror cache for repoURL is ready, then creates a
// per-repo worktree at <taskRoot>/<repoName>/ on a fresh branch off the base
// ref. If the worktree already exists (resuming a prior round), it resets it
// clean and checks out a new branch instead. No allowlist: any URL the agent
// passes is cloned (the GitLab PAT is the real permission boundary).
//
// The branch name is role-aware:
//   - role="delivery" → node/<shortNodeRunID> (matches multica's node-branch
//     convention for deliverable PRs; the delivery repo is Gitea-hosted).
//   - role="code" or any other value (including "", the default) →
//     agent/<sanitize(agent)>/<shortTaskID> (the existing code-repo convention).
//
// In practice multica only emits delivery repos for node-run tasks, so
// nodeRunID is always present when role="delivery"; if it is missing we fall
// back to the agent branch rather than producing an unhelpful "node/" prefix.
func (wm *WorkspaceManager) CheckoutRepo(workspaceID, taskRoot, repoURL, agentName, taskID, baseBranch, accessToken, role, nodeRunID string) (string, error) {
	if err := validateID(workspaceID); err != nil {
		return "", err
	}
	if repoURL == "" {
		return "", fmt.Errorf("checkout: empty repo url")
	}
	if err := validateID(repoName(repoURL)); err != nil {
		return "", fmt.Errorf("invalid repo url %q: %w", repoURL, err)
	}
	cache, err := wm.EnsureRepoReady(workspaceID, repoURL, accessToken)
	if err != nil {
		return "", err
	}
	baseRef, err := wm.resolveBaseRef(cache, baseBranch)
	if err != nil {
		return "", err
	}
	dir := RepoWorktreeDir(taskRoot, repoURL)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	branchName := deliveryBranch(nodeRunID, agentName, taskID, role)
	if _, err := os.Stat(dir); err == nil {
		// Existing worktree (prior round): reset + new branch.
		if isGitWorktree(dir) {
			if err := wm.ResetWorktree(dir, branchName, baseRef); err != nil {
				return "", err
			}
			return dir, nil
		}
		// Stale non-worktree dir in the way: remove and rebuild.
		_ = os.RemoveAll(dir)
	}
	// Fresh worktree on a new branch. Collision => timestamp suffix retry.
	if err := runGit("-C", cache, "worktree", "add", "-b", branchName, dir, baseRef); err != nil {
		if isBranchCollision(err) {
			branchName = fmt.Sprintf("%s-%d", branchName, time.Now().Unix())
			if err := runGit("-C", cache, "worktree", "add", "-b", branchName, dir, baseRef); err != nil {
				return "", fmt.Errorf("add worktree: %w", err)
			}
		} else {
			return "", fmt.Errorf("add worktree: %w", err)
		}
	}
	return dir, nil
}

// deliveryBranch picks the worktree branch for a repo checkout based on its
// role. A "delivery" role with a populated nodeRunID produces the multica
// node-branch convention (node/<shortNodeRunID>); everything else falls back to
// the code-repo agent branch.
func deliveryBranch(nodeRunID, agentName, taskID, role string) string {
	if role == "delivery" && nodeRunID != "" {
		return "node/" + shortID(nodeRunID)
	}
	return agentBranch(agentName, taskID)
}

// isGitWorktree reports whether dir is an active git worktree (has a .git file
// pointing at the worktree metadata, or a .git directory).
func isGitWorktree(dir string) bool {
	gitPath := filepath.Join(dir, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return false
	}
	if fi.IsDir() {
		return true
	}
	b, err := os.ReadFile(gitPath)
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(b)), "gitdir:")
}
