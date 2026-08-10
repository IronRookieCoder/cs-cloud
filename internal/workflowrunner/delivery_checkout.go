package workflowrunner

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/workflow"
)

// prepareDeliveryRepo deterministically clones (or updates) the task's delivery
// repo so the agent can read upstream deliverables on demand — instead of
// relying on the agent to clone from the .cs-cloud.repos instructions. It
// fetches the latest, updates the inst branch (where approved upstream nodes
// land), then checks out the node branch (the agent's working branch): inst
// first, node second. The agent ends on node with inst available + fresh for
// reading upstream via `git show inst:<path>` etc. Best-effort: an error is
// returned for the caller to log; the agent still has .cs-cloud.repos to clone
// manually, so a transient clone/pull failure does not fail the task.
func prepareDeliveryRepo(ctx context.Context, worktree string, payload workflow.TaskRunPayload, env []string) error {
	var repo *workflow.RepoSpec
	for i := range payload.Repos {
		if strings.EqualFold(payload.Repos[i].Role, "delivery") {
			r := payload.Repos[i]
			repo = &r
			break
		}
	}
	if repo == nil {
		return nil // no delivery repo for this task
	}
	inst := envGet(env, "CS_CLOUD_GITEA_INST_BRANCH")
	node := envGet(env, "CS_CLOUD_GITEA_NODE_BRANCH")
	if inst == "" || node == "" {
		return nil // no branch context — leave cloning to the agent
	}
	token := envGet(env, "CS_CLOUD_GITEA_TOKEN")
	cloneTarget := repoCloneURL(repo.URL, token)
	if cloneTarget == "" {
		return fmt.Errorf("resolve delivery repo URL from %q", repo.URL)
	}
	alias := strings.TrimSpace(repo.Alias)
	if alias == "" {
		alias = "delivery"
	}
	// Validate the alias is a single path component so a value like "../other"
	// (server misconfiguration, or a future caller that lets the agent influence
	// the alias) cannot make git clone/fetch operate outside the task worktree.
	// Same rule the task id already enforces via validateID.
	if err := validateID(alias); err != nil {
		return fmt.Errorf("delivery repo alias %q invalid: %w", alias, err)
	}
	cloneDir := filepath.Join(worktree, alias)

	if !dirExists(cloneDir) {
		if err := runGitCmd(ctx, worktree, "clone", cloneTarget, cloneDir); err != nil {
			return fmt.Errorf("clone delivery repo: %w", err)
		}
	}
	// fetch latest, then force-sync inst first (upstream), then node (working
	// branch). `checkout -B` force-recreates the local branch at the server ref,
	// which is required because the worker force-pushes node each round (rewrites
	// history) — a plain `pull --ff-only` would fail non-ff on rework. node comes
	// from origin/node (not inst) so the worker's prior-round work is preserved
	// and the agent pushes the EXISTING node→inst MR forward (per the rework
	// design); upstream merged after the node was cut stays readable via inst.
	for _, step := range [][]string{
		{"fetch", "origin"},
		{"checkout", "-B", inst, "origin/" + inst},
		{"checkout", "-B", node, "origin/" + node},
	} {
		if err := runGitCmd(ctx, cloneDir, step...); err != nil {
			return fmt.Errorf("delivery repo %v: %w", step, err)
		}
	}
	return nil
}

// envGet reads a KEY=VALUE entry from a flat env slice.
func envGet(env []string, key string) string {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return strings.TrimPrefix(e, prefix)
		}
	}
	return ""
}

// repoCloneURL returns a clone URL with the token embedded for http(s) Gitea;
// other schemes (file://, ssh://) and empty tokens are returned unchanged so
// local/test clones work without auth.
func repoCloneURL(rawURL, token string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return strings.TrimSpace(rawURL)
	}
	if (u.Scheme == "http" || u.Scheme == "https") && token != "" {
		u.User = url.UserPassword("oauth2", token)
		return u.String()
	}
	return strings.TrimSpace(rawURL)
}

var credURLRe = regexp.MustCompile(`(\w+://[^/:@]+:)[^@]+(@)`)

// runGitCmd runs git in dir ("" = inherit cwd) and returns an error whose
// message has any URL-embedded credentials redacted so tokens never reach logs.
func runGitCmd(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w: %s", args[0], err, credURLRe.ReplaceAllString(strings.TrimSpace(stderr.String()), "${1}***${2}"))
	}
	return nil
}

// logDeliveryRepoPrepare is the caller's best-effort hook: a clone/pull failure
// is logged but does not fail the task (the agent can still clone manually from
// .cs-cloud.repos).
func logDeliveryRepoPrepare(err error) {
	if err != nil {
		logger.Warn("workflow: prepare delivery repo failed (agent may need to clone manually from .cs-cloud.repos): %v", err)
	}
}
