package workflowrunner

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"cs-cloud/internal/workflow"
)

type repoDownloadObservation struct {
	repo  workflow.RepoSpec
	path  string
	found bool
	err   string
}

func observeRepoDownloads(worktree string, repos []workflow.RepoSpec) []repoDownloadObservation {
	if len(repos) == 0 || strings.TrimSpace(worktree) == "" {
		return nil
	}
	remotes := discoverGitRemotes(worktree)
	observations := make([]repoDownloadObservation, 0, len(repos))
	for _, repo := range repos {
		observation := repoDownloadObservation{repo: repo}
		want := normalizeRepoURL(repo.URL)
		if want == "" {
			observation.err = "invalid-url"
			observations = append(observations, observation)
			continue
		}
		for _, remote := range remotes {
			if remote.normalized == want {
				observation.path = remote.path
				observation.found = true
				break
			}
		}
		observations = append(observations, observation)
	}
	return observations
}

type discoveredGitRemote struct {
	path       string
	normalized string
}

func discoverGitRemotes(root string) []discoveredGitRemote {
	var remotes []discoveredGitRemote
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || !entry.IsDir() {
			return nil
		}
		if path != root {
			switch entry.Name() {
			case ".git", "node_modules", ".next", ".turbo":
				return filepath.SkipDir
			}
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			return nil
		}
		remote := gitRemoteOrigin(path)
		if remote == "" {
			return nil
		}
		remotes = append(remotes, discoveredGitRemote{
			path:       path,
			normalized: normalizeRepoURL(remote),
		})
		return filepath.SkipDir
	})
	return remotes
}

func gitRemoteOrigin(dir string) string {
	cmd := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func normalizeRepoURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	u.User = nil
	u.Fragment = ""
	u.RawQuery = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), ".git")
	return u.String()
}

func repoDownloadObservationSummary(observations []repoDownloadObservation) string {
	if len(observations) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(observations))
	for _, observation := range observations {
		label := repoObservationLabel(observation.repo)
		switch {
		case observation.found:
			parts = append(parts, label+"=found(path="+observation.path+")")
		case observation.err != "":
			parts = append(parts, label+"=missing("+observation.err+")")
		default:
			parts = append(parts, label+"=missing")
		}
	}
	return "[" + strings.Join(parts, "; ") + "]"
}

func repoDownloadObservationHasMissing(observations []repoDownloadObservation) bool {
	for _, observation := range observations {
		if !observation.found {
			return true
		}
	}
	return false
}

func repoObservationLabel(repo workflow.RepoSpec) string {
	name := firstNonEmptyTrimmed(repo.Alias, repo.BaseBranch, repo.URL)
	if name == repo.URL {
		name = redactURL(name)
	}
	return strings.Join([]string{repo.Role, repo.Provider, name}, ":")
}
