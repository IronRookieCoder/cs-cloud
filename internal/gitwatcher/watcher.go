package gitwatcher

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/model"

	"github.com/fsnotify/fsnotify"
)

// Watcher monitors git repositories using fsnotify on key .git files,
// falling back to a low-frequency poll as a safety net.
type Watcher struct {
	mu         sync.RWMutex
	eventBus   EventEmitter
	watchRepos map[string]*RepoState
	ctx        context.Context
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	fw *fsnotify.Watcher // internal fsnotify for .git internals
}

type RepoState struct {
	Path          string
	CurrentBranch string
	CurrentHead   string
	RemoteHead    string
	LastCommit    string
	LastStatus    string
	IsRepo        bool
	hasUpstream   bool
}

type EventEmitter interface {
	Emit(event agent.Event)
}

// FileWatcher interface kept for API compatibility; no longer used internally.
type FileWatcher interface {
	AddDirectory(path string) error
	RemoveDirectory(path string) error
}

// New creates a new git watcher.
func New(eventBus EventEmitter) *Watcher {
	return &Watcher{
		eventBus:   eventBus,
		watchRepos: make(map[string]*RepoState),
	}
}

// Start begins watching git repositories.
func (w *Watcher) Start(ctx context.Context, _ FileWatcher) error {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("gitwatcher: create fsnotify: %w", err)
	}
	w.fw = fw
	w.ctx, w.cancel = context.WithCancel(ctx)

	// Main event loop: processes fsnotify events + fallback ticker.
	w.wg.Add(1)
	go w.eventLoop()

	return nil
}

// eventLoop is the central goroutine that reacts to file changes inside .git
// and periodically runs a fallback check.
func (w *Watcher) eventLoop() {
	defer w.wg.Done()

	fallback := time.NewTicker(10 * time.Second)
	defer fallback.Stop()

	// Collect file change signals per repo; debounced.
	changed := make(map[string]bool)
	var changedMu sync.Mutex

	debounce := time.NewTimer(0)
	<-debounce.C // drain initial

	for {
		select {
		case <-w.ctx.Done():
			return

		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			repo := w.repoForGitPath(ev.Name)
			if repo == "" {
				continue
			}
			changedMu.Lock()
			changed[repo] = true
			changedMu.Unlock()
			debounce.Reset(200 * time.Millisecond)

		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			logger.Error("gitwatcher fsnotify error: %v", err)

		case <-debounce.C:
			changedMu.Lock()
			repos := make(map[string]bool, len(changed))
			for r := range changed {
				repos[r] = true
			}
			clear(changed)
			changedMu.Unlock()

			for repo := range repos {
				w.checkRepo(repo)
			}

		case <-fallback.C:
			w.checkAllRepos()
		}
	}
}

// repoForGitPath returns the watched repo root for a path inside .git,
// or "" if the path doesn't belong to any watched repo.
func (w *Watcher) repoForGitPath(gitPath string) string {
	w.mu.RLock()
	defer w.mu.RUnlock()
	for repoPath := range w.watchRepos {
		gitDir := filepath.Join(repoPath, ".git") + string(filepath.Separator)
		if strings.HasPrefix(gitPath, gitDir) || gitPath == filepath.Join(repoPath, ".git") {
			return repoPath
		}
	}
	return ""
}

// AddRepository adds a git repository to watch.
func (w *Watcher) AddRepository(repoPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.watchRepos[repoPath]; exists {
		return nil
	}

	state := &RepoState{Path: repoPath}
	if err := w.updateRepoState(state); err != nil {
		return fmt.Errorf("failed to initialize repo state: %w", err)
	}
	if !state.IsRepo {
		return fmt.Errorf("not a git repository: %s", repoPath)
	}

	w.watchRepos[repoPath] = state

	// Watch key .git internals.
	w.watchGitInternals(repoPath)

	logger.Info("Started watching git repository: %s (branch: %s)", repoPath, state.CurrentBranch)

	// Emit initial branch event
	w.emitBranchEvent(state.CurrentBranch, "", state.CurrentBranch, repoPath)

	return nil
}

// RemoveRepository removes a git repository from watching.
func (w *Watcher) RemoveRepository(repoPath string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, exists := w.watchRepos[repoPath]; !exists {
		return nil
	}

	w.unwatchGitInternals(repoPath)
	delete(w.watchRepos, repoPath)
	logger.Info("Stopped watching git repository: %s", repoPath)
	return nil
}

// watchGitInternals adds fsnotify watches on the key .git files/dirs.
func (w *Watcher) watchGitInternals(repoPath string) {
	if w.fw == nil {
		return
	}

	gitDir := filepath.Join(repoPath, ".git")

	// .git/HEAD — branch switches, initial checkout
	_ = w.fw.Add(filepath.Join(gitDir, "HEAD"))

	// .git/refs/heads/ — new commits, force-pushes
	// fsnotify watches the directory itself; file changes inside are reported.
	_ = w.fw.Add(filepath.Join(gitDir, "refs"))
	headsDir := filepath.Join(gitDir, "refs", "heads")
	if _, err := os.Stat(headsDir); err == nil {
		_ = w.fw.Add(headsDir)
	}

	// .git/refs/remotes/ — push, fetch, remote branch updates
	remotesDir := filepath.Join(gitDir, "refs", "remotes")
	if _, err := os.Stat(remotesDir); err == nil {
		_ = w.fw.Add(remotesDir)
		filepath.Walk(remotesDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if info.IsDir() {
				_ = w.fw.Add(path)
			}
			return nil
		})
	}

	// .git/index — staging area changes (git add, etc.)
	_ = w.fw.Add(filepath.Join(gitDir, "index"))

	// .git/packed-refs — packed remote refs (used by many git operations)
	packedRefs := filepath.Join(gitDir, "packed-refs")
	if _, err := os.Stat(packedRefs); err == nil {
		_ = w.fw.Add(packedRefs)
	}
}

// unwatchGitInternals removes fsnotify watches for a repo.
func (w *Watcher) unwatchGitInternals(repoPath string) {
	if w.fw == nil {
		return
	}
	gitDir := filepath.Join(repoPath, ".git")
	_ = w.fw.Remove(filepath.Join(gitDir, "HEAD"))
	_ = w.fw.Remove(filepath.Join(gitDir, "refs"))
	_ = w.fw.Remove(filepath.Join(gitDir, "refs", "heads"))

	remotesDir := filepath.Join(gitDir, "refs", "remotes")
	filepath.Walk(remotesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = w.fw.Remove(path)
		}
		return nil
	})

	_ = w.fw.Remove(filepath.Join(gitDir, "index"))
	_ = w.fw.Remove(filepath.Join(gitDir, "packed-refs"))
}

// checkRepo checks a single repo for branch, commit, and status changes.
func (w *Watcher) checkRepo(repoPath string) {
	w.mu.Lock()
	defer w.mu.Unlock()

	state, ok := w.watchRepos[repoPath]
	if !ok {
		return
	}

	oldBranch := state.CurrentBranch
	oldHead := state.CurrentHead
	oldRemoteHead := state.RemoteHead

	if err := w.updateRepoState(state); err != nil {
		logger.Error("Failed to update repo state for %s: %v", repoPath, err)
		return
	}

	if state.CurrentBranch != oldBranch {
		logger.Info("Branch changed in %s: %s -> %s", repoPath, oldBranch, state.CurrentBranch)
		w.emitBranchEvent(state.CurrentBranch, oldBranch, state.CurrentHead, repoPath)
	}

	if state.CurrentHead != oldHead {
		logger.Info("New commit in %s: %s", repoPath, state.CurrentHead)
		w.emitCommitEvent(state, oldHead)
	}

	// Check remote HEAD changes (push, fetch, pull)
	if state.RemoteHead != oldRemoteHead {
		logger.Info("Remote HEAD changed in %s: %s -> %s", repoPath, oldRemoteHead, state.RemoteHead)
		w.emitRemoteEvent(state, oldRemoteHead, repoPath)
	}

	// Check status changes
	status, err := w.runGitCommand(state.Path, "status", "--porcelain")
	if err == nil {
		status = strings.TrimSpace(status)
		if status != state.LastStatus {
			state.LastStatus = status
			if status != "" {
				logger.Debug("Git status changed in %s", repoPath)
				w.emitStatusEvent(state, status)
			}
		}
	}
}

// checkAllRepos is the fallback that checks every watched repo.
func (w *Watcher) checkAllRepos() {
	w.mu.RLock()
	paths := make([]string, 0, len(w.watchRepos))
	for p := range w.watchRepos {
		paths = append(paths, p)
	}
	w.mu.RUnlock()

	for _, p := range paths {
		w.checkRepo(p)
	}
}

// updateRepoState reads current branch and HEAD from git.
func (w *Watcher) updateRepoState(state *RepoState) error {
	gitDir := filepath.Join(state.Path, ".git")
	if _, err := os.Stat(gitDir); os.IsNotExist(err) {
		state.IsRepo = false
		return nil
	}
	state.IsRepo = true

	branch, err := w.runGitCommand(state.Path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get branch: %w", err)
	}
	state.CurrentBranch = strings.TrimSpace(branch)

	head, err := w.runGitCommand(state.Path, "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}
	state.CurrentHead = strings.TrimSpace(head)

	// Get remote HEAD using @{u} (upstream tracking branch) for push/fetch detection
	if state.CurrentBranch != "HEAD" && state.CurrentBranch != "" {
		// Use @{u} to get the actual upstream branch (e.g., origin/main)
		upstream, err := w.runGitCommand(state.Path, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}")
		if err == nil {
			upstream = strings.TrimSpace(upstream)
			if upstream != "" && upstream != "@{u}" {
				remoteHead, err := w.runGitCommand(state.Path, "rev-parse", "--verify", upstream)
				if err == nil {
					oldHead := state.RemoteHead
					state.RemoteHead = strings.TrimSpace(remoteHead)
					if !state.hasUpstream {
						logger.Debug("Upstream configured for %s: %s", state.CurrentBranch, upstream)
						state.hasUpstream = true
					}
					if state.RemoteHead != oldHead {
						logger.Debug("Remote HEAD for %s (upstream: %s): %s (was: %s)", state.CurrentBranch, upstream, state.RemoteHead, oldHead)
					}
				} else {
					logger.Debug("Failed to get remote HEAD for %s: %v", upstream, err)
					state.RemoteHead = ""
				}
			} else {
				if state.hasUpstream {
					logger.Debug("Upstream removed for %s", state.CurrentBranch)
					state.hasUpstream = false
				}
				state.RemoteHead = ""
			}
		} else {
			if state.hasUpstream {
				logger.Debug("Upstream removed for %s", state.CurrentBranch)
				state.hasUpstream = false
			}
			state.RemoteHead = ""
		}
	}

	lastCommit, err := w.runGitCommand(state.Path, "log", "-1", "--format=%H %s")
	if err != nil {
		return fmt.Errorf("failed to get last commit: %w", err)
	}
	state.LastCommit = strings.TrimSpace(lastCommit)

	return nil
}

func (w *Watcher) emitBranchEvent(newBranch, oldBranch, currentHead, repoPath string) {
	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitBranchChanged,
		Data: map[string]any{
			"new_branch":   newBranch,
			"old_branch":   oldBranch,
			"current_head": currentHead,
			"repo_path":    repoPath,
			"timestamp":    time.Now().Unix(),
		},
	})
	logger.Debug("Git branch event emitted: %s -> %s in %s", oldBranch, newBranch, repoPath)
}

func (w *Watcher) emitCommitEvent(state *RepoState, oldHead string) {
	commitDetails, err := w.runGitCommand(state.Path, "log", "-1", "--format=%H|%an|%ae|%s|%ct")
	if err != nil {
		logger.Error("Failed to get commit details: %v", err)
		return
	}
	parts := strings.Split(commitDetails, "|")
	if len(parts) < 5 {
		logger.Error("Invalid commit details format")
		return
	}

	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitCommit,
		Data: map[string]any{
			"hash":         parts[0],
			"author_name":  parts[1],
			"author_email": parts[2],
			"message":      parts[3],
			"timestamp":    parts[4],
			"branch":       state.CurrentBranch,
			"repo_path":    state.Path,
			"emitted_at":   time.Now().Unix(),
		},
	})
	logger.Debug("Git commit event emitted: %s in %s", parts[0], state.Path)
}

func (w *Watcher) emitStatusEvent(state *RepoState, status string) {
	changedFiles := strings.Split(status, "\n")
	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitStatusChanged,
		Data: map[string]any{
			"branch":        state.CurrentBranch,
			"changed_files": changedFiles,
			"repo_path":     state.Path,
			"timestamp":     time.Now().Unix(),
		},
	})
	logger.Debug("Git status event emitted: %d changed files in %s", len(changedFiles), state.Path)
}

func (w *Watcher) emitRemoteEvent(state *RepoState, oldRemoteHead, repoPath string) {
	w.eventBus.Emit(agent.Event{
		Type: model.EventTypeHostGitRemoteChanged,
		Data: map[string]any{
			"branch":       state.CurrentBranch,
			"old_head":     oldRemoteHead,
			"new_head":     state.RemoteHead,
			"repo_path":    repoPath,
			"timestamp":    time.Now().Unix(),
		},
	})
	logger.Debug("Git remote event emitted: %s -> %s in %s", oldRemoteHead, state.RemoteHead, repoPath)
}

func (w *Watcher) runGitCommand(dir string, args ...string) (string, error) {
	fullArgs := append([]string{"-c", "core.quotePath=false"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", err
	}

	var stdoutBuf strings.Builder
	stdoutScanner := bufio.NewScanner(stdout)
	for stdoutScanner.Scan() {
		stdoutBuf.WriteString(stdoutScanner.Text())
		stdoutBuf.WriteString("\n")
	}

	var stderrBuf strings.Builder
	stderrScanner := bufio.NewScanner(stderr)
	for stderrScanner.Scan() {
		stderrBuf.WriteString(stderrScanner.Text())
		stderrBuf.WriteString("\n")
	}

	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("git command failed: %w, stderr: %s", err, stderrBuf.String())
	}
	return stdoutBuf.String(), nil
}

// Stop stops the git watcher.
func (w *Watcher) Stop() {
	if w == nil {
		return
	}
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	if w.fw != nil {
		w.fw.Close()
	}
	logger.Info("Git watcher stopped")
}

// WatchingRepositories returns list of repositories being watched.
func (w *Watcher) WatchingRepositories() []string {
	if w == nil {
		return nil
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	repos := make([]string, 0, len(w.watchRepos))
	for r := range w.watchRepos {
		repos = append(repos, r)
	}
	return repos
}

// GetRepositoryState returns the current state of a watched repository.
func (w *Watcher) GetRepositoryState(repoPath string) (*RepoState, error) {
	if w == nil {
		return nil, fmt.Errorf("watcher not initialized")
	}
	w.mu.RLock()
	defer w.mu.RUnlock()
	state, exists := w.watchRepos[repoPath]
	if !exists {
		return nil, fmt.Errorf("repository not being watched: %s", repoPath)
	}
	cp := *state
	return &cp, nil
}
