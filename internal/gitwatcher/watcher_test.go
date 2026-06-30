package gitwatcher

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/model"
)

// MockEventBus implements EventEmitter for testing
type MockEventBus struct {
	mu     sync.Mutex
	events []agent.Event
}

func (m *MockEventBus) Emit(event agent.Event) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
}

func (m *MockEventBus) GetEvents() []agent.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]agent.Event{}, m.events...)
}

func (m *MockEventBus) GetEventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func (m *MockEventBus) ClearEvents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

func (m *MockEventBus) GetEventsByType(eventType string) []agent.Event {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []agent.Event
	for _, event := range m.events {
		if event.Type == eventType {
			result = append(result, event)
		}
	}
	return result
}

// setupTestGitRepo creates a temporary git repository for testing
func setupTestGitRepo(t *testing.T) (string, func()) {
	tempDir, err := os.MkdirTemp("", "gitwatcher_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Initialize git repo
	runGitCommand(t, tempDir, "init", "--initial-branch", "main")
	runGitCommand(t, tempDir, "config", "user.name", "Test User")
	runGitCommand(t, tempDir, "config", "user.email", "test@example.com")

	// Create initial commit
	initialFile := filepath.Join(tempDir, "README.md")
	err = os.WriteFile(initialFile, []byte("# Test Repository"), 0644)
	if err != nil {
		t.Fatalf("Failed to create initial file: %v", err)
	}
	runGitCommand(t, tempDir, "add", "README.md")
	runGitCommand(t, tempDir, "commit", "-m", "Initial commit")

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	return tempDir, cleanup
}

func runGitCommand(t *testing.T, dir string, args ...string) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Git command failed: %v\nOutput: %s", err, string(output))
	}
}

// runGitCommandOutput runs a git command and returns its stdout. Fails the test on error.
func runGitCommandOutput(t *testing.T, dir string, args ...string) string {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Git command failed: %v\nOutput: %s", err, string(output))
	}
	return string(output)
}

func TestNewGitWatcher(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	if watcher == nil {
		t.Fatal("New() returned nil watcher")
	}
}

func TestGitWatcher_StartStop(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()

	// Create a mock file watcher
	mockFileWatcher := &mockFileWatcher{}

	// Test start
	err := watcher.Start(ctx, mockFileWatcher)
	if err != nil {
		t.Fatalf("Start() failed: %v", err)
	}

	// Test stop
	watcher.Stop()

	// Multiple stops should be safe
	watcher.Stop()
}

func TestGitWatcher_AddRepository(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Clear initial events from setup
	mockBus.ClearEvents()

	// Add repository
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}

	// Check repository is in watch list
	repos := watcher.WatchingRepositories()
	if len(repos) != 1 {
		t.Errorf("Expected 1 repository, got %d", len(repos))
	}
	if repos[0] != repoPath {
		t.Errorf("Expected %s, got %s", repoPath, repos[0])
	}

	// Check repository state
	state, err := watcher.GetRepositoryState(repoPath)
	if err != nil {
		t.Fatalf("GetRepositoryState() failed: %v", err)
	}

	if !state.IsRepo {
		t.Error("Expected IsRepo to be true")
	}
	if state.CurrentBranch != "main" {
		t.Errorf("Expected branch 'main', got '%s'", state.CurrentBranch)
	}
	if state.CurrentHead == "" {
		t.Error("Expected CurrentHead to be set")
	}

	// Check that branch changed event was emitted
	events := mockBus.GetEventsByType(model.EventTypeHostGitBranchChanged)
	if len(events) == 0 {
		t.Error("Expected branch changed event on repository addition")
	}
}

func TestGitWatcher_NonGitRepository(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create temporary non-git directory
	tempDir, err := os.MkdirTemp("", "gitwatcher_nongit_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Try to add non-git repository
	err = watcher.AddRepository(tempDir)
	if err == nil {
		t.Error("Expected error when adding non-git repository, got nil")
	}

	// Check that repository was not added to watch list
	repos := watcher.WatchingRepositories()
	if len(repos) != 0 {
		t.Errorf("Expected 0 repositories, got %d", len(repos))
	}
}

func TestGitWatcher_RemoveRepository(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Add repository
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}

	// Remove repository
	err = watcher.RemoveRepository(repoPath)
	if err != nil {
		t.Fatalf("RemoveRepository() failed: %v", err)
	}

	// Check repository is removed from watch list
	repos := watcher.WatchingRepositories()
	if len(repos) != 0 {
		t.Errorf("Expected 0 repositories, got %d", len(repos))
	}

	// Verify repository state is no longer available
	_, err = watcher.GetRepositoryState(repoPath)
	if err == nil {
		t.Error("Expected error when getting removed repository state, got nil")
	}
}

func TestGitWatcher_BranchDetection(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Clear initial events
	mockBus.ClearEvents()

	// Add repository
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}

	// Clear events from repository addition
	mockBus.ClearEvents()

	// Create and checkout a new branch
	newBranch := "test-branch"
	runGitCommand(t, repoPath, "checkout", "-b", newBranch)

	// Wait for fsnotify event + debounce (event-driven, fast)
	time.Sleep(1 * time.Second)

	// Check for branch changed event
	events := mockBus.GetEventsByType(model.EventTypeHostGitBranchChanged)
	if len(events) == 0 {
		t.Error("Expected branch changed event after branch switch")
	} else {
		// Verify the event contains the new branch
		found := false
		for _, event := range events {
			if data, ok := event.Data.(map[string]any); ok {
				if newBranchVal, ok := data["new_branch"].(string); ok && newBranchVal == newBranch {
					found = true
					break
				}
			}
		}
		if !found {
			t.Errorf("Branch changed event for '%s' not found", newBranch)
		}
	}

	// Check repository state reflects new branch
	state, err := watcher.GetRepositoryState(repoPath)
	if err != nil {
		t.Fatalf("GetRepositoryState() failed: %v", err)
	}

	if state.CurrentBranch != newBranch {
		t.Errorf("Expected branch '%s', got '%s'", newBranch, state.CurrentBranch)
	}
}

func TestGitWatcher_CommitDetection(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Add repository
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}

	// Clear events from repository addition
	mockBus.ClearEvents()

	// Create a new commit
	testFile := filepath.Join(repoPath, "test.txt")
	err = os.WriteFile(testFile, []byte("test content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	runGitCommand(t, repoPath, "add", "test.txt")
	runGitCommand(t, repoPath, "commit", "-m", "Test commit")

	// Wait for fsnotify event + debounce (event-driven, fast)
	time.Sleep(1 * time.Second)

	// Check for commit event
	events := mockBus.GetEventsByType(model.EventTypeHostGitCommit)
	if len(events) == 0 {
		t.Error("Expected commit event after new commit")
	} else {
		// Verify the event contains commit details
		found := false
		for _, event := range events {
			if data, ok := event.Data.(map[string]any); ok {
				if message, ok := data["message"].(string); ok && message == "Test commit" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("Commit event with 'Test commit' message not found")
		}
	}
}

func TestGitWatcher_StatusDetection(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Add repository
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}

	// Clear events from repository addition
	mockBus.ClearEvents()

	// Create a file but don't commit it (creates status change)
	testFile := filepath.Join(repoPath, "uncommitted.txt")
	err = os.WriteFile(testFile, []byte("uncommitted content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	// Directly trigger checkRepo instead of waiting 12s for the fallback poller.
	// This matches the pattern used by other event tests and avoids flaky timing.
	watcher.checkRepo(repoPath)

	// Check for status changed event
	events := mockBus.GetEventsByType(model.EventTypeHostGitStatusChanged)
	if len(events) == 0 {
		t.Error("Expected status changed event after uncommitted file")
	} else {
		// Verify the event contains changed files
		found := false
		for _, event := range events {
			if data, ok := event.Data.(map[string]any); ok {
				if changedFiles, ok := data["changed_files"].([]string); ok && len(changedFiles) > 0 {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("Status changed event with changed files not found")
		}
	}
}

func TestGitWatcher_StashDetection(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository (already has an initial commit)
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Add repository
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}
	mockBus.ClearEvents()

	// Create a worktree change and stash it. We need a tracked/modified file
	// so that `git stash` has something to store.
	stashFile := filepath.Join(repoPath, "stash-me.txt")
	err = os.WriteFile(stashFile, []byte("stash content"), 0644)
	if err != nil {
		t.Fatalf("Failed to create stash file: %v", err)
	}
	runGitCommand(t, repoPath, "add", "stash-me.txt")
	runGitCommand(t, repoPath, "stash", "push", "-m", "test stash")

	// Directly trigger checkRepo to detect the stash change.
	watcher.checkRepo(repoPath)

	// Expect a stash changed event with count == 1.
	events := mockBus.GetEventsByType(model.EventTypeHostGitStashChanged)
	if len(events) == 0 {
		t.Fatal("Expected stash changed event after git stash push")
	}
	if len(events) > 1 {
		t.Fatalf("Expected exactly 1 stash event, got %d", len(events))
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", events[0].Data)
	}
	if count, ok := data["count"].(int); !ok || count != 1 {
		t.Errorf("Expected count == 1, got %v", data["count"])
	}

	// Now drop the stash and verify a second event with count == 0 is emitted.
	mockBus.ClearEvents()
	runGitCommand(t, repoPath, "stash", "drop")
	watcher.checkRepo(repoPath)

	events = mockBus.GetEventsByType(model.EventTypeHostGitStashChanged)
	if len(events) == 0 {
		t.Fatal("Expected stash changed event after git stash drop")
	}
	data, ok = events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", events[0].Data)
	}
	if count, ok := data["count"].(int); !ok || count != 0 {
		t.Errorf("Expected count == 0 after drop, got %v", data["count"])
	}
}

func TestGitWatcher_RemoteDetection(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create test git repository (already has an initial commit on main).
	repoPath, cleanup := setupTestGitRepo(t)
	defer cleanup()

	// Create a bare remote repository.
	remoteDir, remoteCleanup := func() (string, func()) {
		d, err := os.MkdirTemp("", "gitwatcher_remote_test")
		if err != nil {
			t.Fatalf("Failed to create remote temp dir: %v", err)
		}
		runGitCommand(t, d, "init", "--bare")
		return d, func() { os.RemoveAll(d) }
	}()
	defer remoteCleanup()

	// Link local repo to the remote and push to set upstream.
	runGitCommand(t, repoPath, "remote", "add", "origin", remoteDir)
	// Determine current branch dynamically (avoid hardcoding main/master).
	branch := strings.TrimSpace(runGitCommandOutput(t, repoPath, "symbolic-ref", "--short", "HEAD"))
	runGitCommand(t, repoPath, "push", "-u", "origin", branch)

	// Add repository to the watcher (this initializes RemoteHead).
	err := watcher.AddRepository(repoPath)
	if err != nil {
		t.Fatalf("AddRepository() failed: %v", err)
	}
	mockBus.ClearEvents()

	// Create a second commit and push to change the remote HEAD.
	secondFile := filepath.Join(repoPath, "second.txt")
	err = os.WriteFile(secondFile, []byte("second file"), 0644)
	if err != nil {
		t.Fatalf("Failed to create second file: %v", err)
	}
	runGitCommand(t, repoPath, "add", "second.txt")
	runGitCommand(t, repoPath, "commit", "-m", "Second commit")
	runGitCommand(t, repoPath, "push", "origin", branch)

	// Directly trigger checkRepo to detect the remote HEAD change.
	watcher.checkRepo(repoPath)

	events := mockBus.GetEventsByType(model.EventTypeHostGitRemoteChanged)
	if len(events) == 0 {
		t.Fatal("Expected remote changed event after push")
	}
	data, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("Expected map[string]any, got %T", events[0].Data)
	}
	oldHead, _ := data["old_head"].(string)
	newHead, _ := data["new_head"].(string)
	if oldHead == "" || newHead == "" {
		t.Fatalf("Expected non-empty old_head and new_head, got old=%q new=%q", oldHead, newHead)
	}
	if oldHead == newHead {
		t.Errorf("Expected old_head != new_head, both were %s", oldHead)
	}
}

func TestGitWatcher_NilWatcher(t *testing.T) {
	var watcher *Watcher

	// Test that nil watcher doesn't panic for methods that should handle nil safely
	watcher.Stop()
	watcher.WatchingRepositories()

	// Note: AddRepository will panic on nil watcher - this is expected behavior
	// The nil check should be done by the caller
	_, err := watcher.GetRepositoryState("/tmp")
	if err == nil {
		t.Error("Expected error for nil watcher GetRepositoryState, got nil")
	}
}

func TestGitWatcher_MultipleRepositories(t *testing.T) {
	mockBus := &MockEventBus{}
	watcher := New(mockBus)

	ctx := context.Background()
	mockFileWatcher := &mockFileWatcher{}
	watcher.Start(ctx, mockFileWatcher)
	defer watcher.Stop()

	// Create multiple test git repositories
	var repos []string
	var cleanups []func()

	for i := 0; i < 3; i++ {
		repoPath, cleanup := setupTestGitRepo(t)
		repos = append(repos, repoPath)
		cleanups = append(cleanups, cleanup)

		err := watcher.AddRepository(repoPath)
		if err != nil {
			t.Fatalf("AddRepository() failed for repo %d: %v", i, err)
		}
	}

	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()

	// Check all repositories are watched
	watchingRepos := watcher.WatchingRepositories()
	if len(watchingRepos) != 3 {
		t.Errorf("Expected 3 repositories, got %d", len(watchingRepos))
	}

	// Verify all our repositories are in the watch list
	repoMap := make(map[string]bool)
	for _, repo := range watchingRepos {
		repoMap[repo] = true
	}

	for _, expectedRepo := range repos {
		if !repoMap[expectedRepo] {
			t.Errorf("Repository %s not found in watch list", expectedRepo)
		}
	}
}

// mockFileWatcher implements FileWatcher interface for testing
type mockFileWatcher struct {
	mu      sync.Mutex
	watched map[string]bool
}

func (m *mockFileWatcher) AddDirectory(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watched == nil {
		m.watched = make(map[string]bool)
	}
	m.watched[path] = true
	return nil
}

func (m *mockFileWatcher) RemoveDirectory(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watched == nil {
		return nil
	}
	delete(m.watched, path)
	return nil
}
