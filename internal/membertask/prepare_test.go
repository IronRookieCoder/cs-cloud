package membertask

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReworkReusesDirectoryAndKeepsLocalOutput(t *testing.T) {
	var attempt atomic.Int32
	attempt.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := attempt.Load()
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":%d,"task_version":%d,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"result","kind":"file","source_version":"v1","relative_path":"output/result.md","role":"output_writable","content":"draft"}]}`, value, value)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	first, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(first.Directory, "output", "result.md")
	if err := os.WriteFile(output, []byte("local result"), 0o600); err != nil {
		t.Fatal(err)
	}
	attempt.Store(2)
	second, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatalf("rework Prepare: %v", err)
	}
	if second.Directory != first.Directory || second.Attempt != 2 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	data, _ := os.ReadFile(output)
	if string(data) != "local result" {
		t.Fatalf("local output was replaced: %q", data)
	}
}

func TestReworkDoesNotDiscardPendingOperation(t *testing.T) {
	var attempt atomic.Int32
	attempt.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := attempt.Load()
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":%d,"task_version":%d,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`, value, value)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	if _, err := svc.Prepare(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	record, _, _ := store.LoadTask(key)
	record.Operation = &Operation{ID: "operation-1", Status: OperationAccepted}
	record.Ended = true
	if err := store.SaveTask(record); err != nil {
		t.Fatal(err)
	}
	attempt.Store(2)

	_, err := svc.Prepare(context.Background(), key)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "operation_recovery_required" {
		t.Fatalf("Prepare error = %#v", err)
	}
	stored, _, _ := store.LoadTask(key)
	if stored.Operation == nil || stored.Operation.ID != "operation-1" || !stored.Ended {
		t.Fatalf("pending operation was discarded: %+v", stored)
	}
}

func TestReworkRejectsDecreasingAttempt(t *testing.T) {
	var attempt atomic.Int32
	attempt.Store(2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		value := attempt.Load()
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":%d,"task_version":%d,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`, value, value)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	if _, err := svc.Prepare(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	attempt.Store(1)
	_, err := svc.Prepare(context.Background(), key)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_cloud_response" {
		t.Fatalf("Prepare error = %#v", err)
	}
	stored, _, _ := store.LoadTask(key)
	if stored.Attempt != 2 {
		t.Fatalf("stored attempt = %d, want 2", stored.Attempt)
	}
}

func TestUpdatePreparedMetadataRejectsDirectoryOutsideManagedRoot(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	record := TaskRecord{Key: key, PreparationRoot: store.Layout().TasksRoot(), Directory: t.TempDir()}
	err := updatePreparedMetadata(store, record, RemoteTaskContext{})
	te, ok := err.(*TaskError)
	if !ok || te.Code != "unsafe_task_path" {
		t.Fatalf("updatePreparedMetadata error = %#v", err)
	}
}

func TestRemovePrepareJournalRejectsInvalidID(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, nil)
	outside := filepath.Join(store.Layout().StoreRoot(), "outside.json")
	if err := os.WriteFile(outside, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := svc.removePrepareJournal("../outside"); err == nil {
		t.Fatal("removePrepareJournal accepted invalid ID")
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside journal was removed: %v", err)
	}
}

func TestPrepareWithFactsReportsRepeatedPreparationAsAlreadyCompleted(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"result","kind":"file","source_version":"v1","relative_path":"output/result.md","role":"output_writable","content":"draft"}]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")

	first, err := svc.PrepareWithFacts(context.Background(), key, PrepareOptions{})
	if err != nil || !first.Performed || first.Outcome != OutcomeCompleted {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	second, err := svc.PrepareWithFacts(context.Background(), key, PrepareOptions{})
	if err != nil || second.Performed || second.Outcome != OutcomeAlreadyCompleted || !second.Prepared {
		t.Fatalf("second = %+v, err=%v", second, err)
	}
}

func TestConcurrentPrepareAndDeleteAreSerializedByTaskKey(t *testing.T) {
	prepareStarted := make(chan struct{})
	releasePrepare := make(chan struct{})
	var startedOnce sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(prepareStarted) })
		<-releasePrepare
		_, _ = w.Write([]byte(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"result","kind":"file","source_version":"v1","relative_path":"output/result.md","role":"output_writable","content":"draft"}]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")

	prepareResult := make(chan error, 1)
	go func() {
		_, err := svc.Prepare(context.Background(), key)
		prepareResult <- err
	}()
	<-prepareStarted

	deleteResult := make(chan error, 1)
	go func() {
		_, err := svc.PreviewDelete(context.Background(), key, DeleteModeNormal)
		deleteResult <- err
	}()
	select {
	case err := <-deleteResult:
		close(releasePrepare)
		<-prepareResult
		t.Fatalf("PreviewDelete completed while Prepare was in progress: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(releasePrepare)
	if err := <-prepareResult; err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := <-deleteResult; err != nil {
		t.Fatalf("PreviewDelete: %v", err)
	}
}

func TestReprepareArchivesCleanDirectoryWhenContextChanges(t *testing.T) {
	var contextVersion atomic.Int32
	contextVersion.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := contextVersion.Load()
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":%d,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"requirements","kind":"file","source_version":"v%d","relative_path":"input/requirements.md","role":"input_protected","content":"version %d"}]}`, version, version, version)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	first, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	contextVersion.Store(2)
	second, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatalf("reprepare: %v", err)
	}
	if second.Directory != first.Directory || second.RemoteVersion != "1:1:2" {
		t.Fatalf("second = %+v", second)
	}
	data, _ := os.ReadFile(filepath.Join(second.Directory, "input", "requirements.md"))
	if string(data) != "version 2" {
		t.Fatalf("new material = %q", data)
	}
	archives, err := filepath.Glob(filepath.Join(store.Layout().HistoryRoot(), "cloud", "ws", "node-worker", "*"))
	if err != nil || len(archives) != 1 {
		t.Fatalf("archives = %v, err=%v", archives, err)
	}
	old, _ := os.ReadFile(filepath.Join(archives[0], "input", "requirements.md"))
	if string(old) != "version 1" {
		t.Fatalf("archived material = %q", old)
	}
}

func TestReprepareKeepsCustomDirectoryAndArchivesWithinManagedRoot(t *testing.T) {
	var contextVersion atomic.Int32
	contextVersion.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := contextVersion.Load()
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":%d,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"requirements","kind":"file","source_version":"v%d","relative_path":"input/requirements.md","role":"input_protected","content":"version %d"}]}`, version, version, version)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	workDir := t.TempDir()
	first, err := svc.PrepareWithFacts(context.Background(), key, PrepareOptions{WorkDir: workDir})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(first.PreparationRoot, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	contextVersion.Store(2)
	second, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatalf("reprepare: %v", err)
	}
	if second.Directory != first.Directory {
		t.Fatalf("directory moved from %q to %q", first.Directory, second.Directory)
	}
	if _, err := os.Stat(filepath.Join(second.PreparationRoot, ".gitignore")); err != nil {
		t.Fatalf("managed-root .gitignore was not restored: %v", err)
	}
	archive := filepath.Join(first.PreparationRoot, ".history", "cloud", "ws", "node-worker", "attempt-1-context-1")
	if _, err := os.Stat(filepath.Join(archive, "input", "requirements.md")); err != nil {
		t.Fatalf("managed-root archive missing: %v", err)
	}
	if entries, _ := filepath.Glob(filepath.Join(store.Layout().HistoryRoot(), "cloud", "ws", "node-worker", "*")); len(entries) != 0 {
		t.Fatalf("custom task archived in profile history: %v", entries)
	}
}

func TestPrepareWithWorkDirRepairsRecordWithEmptyDirectory(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	if err := store.SaveTask(TaskRecord{Key: key, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	transition, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).PrepareWithFacts(context.Background(), key, PrepareOptions{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatalf("PrepareWithFacts: %v", err)
	}
	if transition.Directory == "" || !transition.Prepared {
		t.Fatalf("transition = %+v", transition)
	}
}

func TestPrepareTargetRejectsWorkDirInsideProfileStore(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, nil)
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	_, _, err := svc.prepareTarget(key, store.Layout().StoreRoot())
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_workdir" {
		t.Fatalf("prepareTarget error = %#v", err)
	}
}

func TestPrepareMetadataRefreshPersistsDisplayName(t *testing.T) {
	var version atomic.Int32
	version.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := version.Load()
		name := ""
		if current == 2 {
			name = `,"display_name":"修复后的名称"`
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"%s,"attempt":1,"task_version":%d,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`, name, current)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	if _, err := svc.Prepare(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	version.Store(2)
	if _, err := svc.Prepare(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.LoadTask(key)
	if err != nil || !found || record.DisplayName != "修复后的名称" || record.DisplayNameSource != DisplayNameSourceCloud {
		t.Fatalf("record=%+v found=%t err=%v", record, found, err)
	}
}

func TestReprepareKeepsCachedDisplayNameWhenCloudOmitsIt(t *testing.T) {
	var contextVersion atomic.Int32
	contextVersion.Store(1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := contextVersion.Load()
		name := ""
		if current == 1 {
			name = `,"display_name":"缓存名称"`
		}
		_, _ = w.Write([]byte(fmt.Sprintf(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"%s,"attempt":1,"task_version":1,"context_version":%d,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`, name, current)))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	if _, err := svc.Prepare(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	contextVersion.Store(2)
	record, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if record.DisplayName != "缓存名称" || record.DisplayNameSource != DisplayNameSourceCloud {
		t.Fatalf("record = %+v", record)
	}
}

func TestPrepareRejectsUnsupportedProviderBeforeCreatingTaskDirectories(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":false,"providers":["github"]}`)
	defer srv.Close()
	root := t.TempDir()
	store, _ := OpenStore(root)
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	_, err := svc.Prepare(context.Background(), key)
	te, ok := err.(*TaskError)
	if !ok || te.Code != "provider_not_supported" {
		t.Fatalf("Prepare error = %#v", err)
	}
	if _, err := os.Stat(store.Layout().TasksRoot()); !os.IsNotExist(err) {
		t.Fatalf("tasks root exists: %v", err)
	}
	if _, err := os.Stat(store.Layout().StagingRoot()); !os.IsNotExist(err) {
		t.Fatalf("staging root exists: %v", err)
	}
}

func TestPrepareRejectsUnsafeRepositoryCloneURL(t *testing.T) {
	for _, cloneURL := range []string{"file:///tmp/repo", "http://gitea.test/team/repo.git", "https://user:secret@gitea.test/team/repo.git", "C:/repo"} {
		t.Run(cloneURL, func(t *testing.T) {
			err := cloneExactRepository(context.Background(), filepath.Join(t.TempDir(), "repo"), RepositoryContext{CloneURL: cloneURL, BaseSHA: "base"})
			te, ok := err.(*TaskError)
			if !ok || te.Code != "unsafe_repository_url" {
				t.Fatalf("error = %#v, want unsafe_repository_url", err)
			}
		})
	}
}

func TestCloneExactRepositoryRejectsInvalidBaseSHABeforeGit(t *testing.T) {
	err := cloneExactRepository(context.Background(), filepath.Join(t.TempDir(), "repo"), RepositoryContext{
		CloneURL: "https://example.invalid/team/repo.git",
		BaseSHA:  "--upload-pack=malicious",
	})
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_cloud_response" {
		t.Fatalf("error = %#v, want invalid_cloud_response", err)
	}
}

func TestCloneExactRepositoryDisablesInteractiveAuthentication(t *testing.T) {
	previous := gitCommandContext
	t.Cleanup(func() { gitCommandContext = previous })
	t.Setenv("GO_WANT_GIT_HELPER", "1")
	gitCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestGitHelperProcess", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}

	err := cloneExactRepository(context.Background(), filepath.Join(t.TempDir(), "repo"), RepositoryContext{
		CloneURL: "https://gitea.example/team/repo.git",
		BaseSHA:  strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatalf("cloneExactRepository: %v", err)
	}
}

func TestCloneExactRepositoryReturnsSanitizedBoundedGitDiagnostic(t *testing.T) {
	previous := gitCommandContext
	t.Cleanup(func() { gitCommandContext = previous })
	t.Setenv("GO_WANT_GIT_HELPER", "1")
	t.Setenv("GO_GIT_HELPER_FAIL", "1")
	gitCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		helperArgs := append([]string{"-test.run=TestGitHelperProcess", "--"}, args...)
		return exec.CommandContext(ctx, os.Args[0], helperArgs...)
	}

	err := cloneExactRepository(context.Background(), filepath.Join(t.TempDir(), "repo"), RepositoryContext{
		CloneURL: "https://gitea.example/team/repo.git",
		BaseSHA:  strings.Repeat("a", 40),
	})
	te, ok := err.(*TaskError)
	if !ok || !strings.Contains(te.Cause, "authentication rejected") {
		t.Fatalf("error = %#v, want Git diagnostic", err)
	}
	if strings.Contains(te.Cause, "secret") || strings.Contains(te.Cause, "\x1b") {
		t.Fatalf("unsafe Git diagnostic = %q", te.Cause)
	}
}

func TestGitHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_GIT_HELPER") != "1" {
		return
	}
	if value, ok := os.LookupEnv("GIT_TERMINAL_PROMPT"); !ok || value != "0" {
		t.Fatalf("GIT_TERMINAL_PROMPT = %q, present=%t", value, ok)
	}
	if value, ok := os.LookupEnv("GIT_ASKPASS"); !ok || value != "" {
		t.Fatalf("GIT_ASKPASS = %q, present=%t", value, ok)
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || separator+1 >= len(os.Args) {
		t.Fatal("git helper arguments are missing")
	}
	args := os.Args[separator+1:]
	if os.Getenv("GO_GIT_HELPER_FAIL") == "1" {
		t.Fatalf("\x1b[31mauthentication rejected for https://user:secret@gitea.example\x1b[0m")
	}
	if args[0] == "clone" {
		if err := os.MkdirAll(args[len(args)-1], 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareMaterializesFilesAndPublishesAtomically(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"requirements","kind":"file","source_version":"v1","relative_path":"input/requirements.md","role":"input_protected","content":"build it"},{"identity":"result","kind":"file","source_version":"v1","relative_path":"output/result.md","role":"output_writable","content":"draft"}]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	record, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if !record.Prepared || record.Activity != ActivityPrepared {
		t.Fatalf("record = %+v", record)
	}
	data, err := os.ReadFile(filepath.Join(record.Directory, "input", "requirements.md"))
	if err != nil || string(data) != "build it" {
		t.Fatalf("material = %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(record.Directory, "manifest.json")); err != nil {
		t.Fatalf("manifest missing: %v", err)
	}
}

func TestPrepareWithWorkDirCreatesManagedTaskDirectory(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"materials":[{"identity":"requirements","kind":"file","source_version":"v1","relative_path":"input/requirements.md","role":"input_protected","content":"build it"}]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	workDir := t.TempDir()
	resolvedWorkDir, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}

	transition, err := svc.PrepareWithFacts(context.Background(), key, PrepareOptions{WorkDir: workDir})
	if err != nil {
		t.Fatalf("PrepareWithFacts: %v", err)
	}
	want := filepath.Join(resolvedWorkDir, ".cs-cloud-tasks", "cloud", "ws", "node-worker")
	if transition.Directory != want || transition.PreparationRoot != filepath.Join(resolvedWorkDir, ".cs-cloud-tasks") {
		t.Fatalf("transition = %+v, want directory %q", transition, want)
	}
	if _, err := os.Stat(filepath.Join(want, "input", "requirements.md")); err != nil {
		t.Fatalf("prepared material missing: %v", err)
	}
}

func TestPrepareRejectsRelativeWorkDir(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")

	_, err := svc.PrepareWithFacts(context.Background(), key, PrepareOptions{WorkDir: "."})
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_workdir" {
		t.Fatalf("error = %#v", err)
	}
}

func TestPrepareTargetRejectsWhitespaceWorkDir(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, nil)
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	_, _, err := svc.prepareTarget(key, "   ")
	te, ok := err.(*TaskError)
	if !ok || te.Code != "invalid_workdir" {
		t.Fatalf("error = %#v", err)
	}
}

func TestPreparePersistsSanitizedDeliverableOrigin(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"critic","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"],"predecessor_results":[{"id":"result-1","title":"实现结果","content":"done","url":"https://gitea.example/team/repo/pulls/7?token=secret#discussion"}]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/critic")

	record, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if record.Manifest == nil || len(record.Manifest.Files) != 1 || record.Manifest.Files[0].Origin == nil {
		t.Fatalf("manifest = %+v", record.Manifest)
	}
	origin := record.Manifest.Files[0].Origin
	if origin.Title != "实现结果" || origin.SourceURL != "https://gitea.example/team/repo/pulls/7" || origin.RelativePath != "input/predecessors/result-1.md" {
		t.Fatalf("origin = %+v", origin)
	}
}

func TestPrepareServerFileContextPreservesCloudMaterialDigestForPreview(t *testing.T) {
	var previewRequest SubmitPreviewRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/member/tasks/node/worker/context":
			_, _ = w.Write([]byte(`{"ref":{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"},"objective":"Build the feature","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"material_digest":"server-material","required_deliverables":[{"id":"delivery-1","title":"Result","required":true,"missing":true}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/member/tasks/node/worker/submit/preview":
			previewRequest = decodeBody[SubmitPreviewRequest](t, r)
			_ = json.NewEncoder(w).Encode(Preview{ID: "preview-1", Kind: "submit", Status: "previewed", Attempt: previewRequest.Attempt, TaskVersion: previewRequest.TaskVersion, ContextVersion: previewRequest.ContextVersion, MaterialDigest: previewRequest.MaterialDigest, ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	record, err := svc.Prepare(context.Background(), key)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if record.CloudMaterialDigest != "server-material" {
		t.Fatalf("CloudMaterialDigest = %q", record.CloudMaterialDigest)
	}
	deliverable := filepath.Join(record.Directory, "output", "deliverables", "delivery-1.md")
	if _, err := os.Stat(deliverable); err != nil {
		t.Fatalf("deliverable missing: %v", err)
	}
	if err := os.WriteFile(deliverable, []byte("completed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PreviewSubmit(context.Background(), key); err != nil {
		t.Fatalf("PreviewSubmit: %v", err)
	}
	if previewRequest.MaterialDigest != "server-material" {
		t.Fatalf("preview material_digest = %q", previewRequest.MaterialDigest)
	}
}

func TestRecoverPrepareJournalDiscardsPreparingStaging(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, nil)
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	staging := filepath.Join(store.Layout().TasksRoot(), "cloud", "ws", ".node-worker.staging-test")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := PrepareJournal{ID: "prepare-1", Key: key, Phase: PreparePhasePreparing, Staging: staging, Final: store.Layout().TaskDir(key), CreatedAt: time.Now()}
	writePrepareJournal(t, store, journal)
	if err := svc.RecoverPrepareJournals(context.Background()); err != nil {
		t.Fatalf("RecoverPrepareJournals: %v", err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("staging still exists: %v", err)
	}
}

func TestRecoverPrepareJournalPublishesReadyStaging(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, nil)
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	final := store.Layout().TaskDir(key)
	staging := filepath.Join(filepath.Dir(final), ".node-worker.staging-test")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := preparedMetadata{Key: key, RemoteVersion: "1:1:1", Attempt: 1}
	b, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(staging, "task.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{SchemaVersion: SchemaVersion, MaterialDigest: "digest"}
	b, _ = json.Marshal(manifest)
	if err := os.WriteFile(filepath.Join(staging, "manifest.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	journal := PrepareJournal{ID: "prepare-2", Key: key, Phase: PreparePhasePublishReady, Staging: staging, Final: final, CreatedAt: time.Now()}
	writePrepareJournal(t, store, journal)
	if err := svc.RecoverPrepareJournals(context.Background()); err != nil {
		t.Fatalf("RecoverPrepareJournals: %v", err)
	}
	record, found, err := store.LoadTask(key)
	if err != nil || !found || record.Directory != final {
		t.Fatalf("record = %+v, found=%v, err=%v", record, found, err)
	}
}

func TestRecoverPrepareJournalRejectsStagingAndFinalTogether(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, nil)
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	final := store.Layout().TaskDir(key)
	staging := filepath.Join(filepath.Dir(final), ".node-worker.staging-test")
	_ = os.MkdirAll(final, 0o700)
	_ = os.MkdirAll(staging, 0o700)
	writePrepareJournal(t, store, PrepareJournal{ID: "prepare-3", Key: key, Phase: PreparePhasePublishReady, Staging: staging, Final: final})
	err := svc.RecoverPrepareJournals(context.Background())
	te, ok := err.(*TaskError)
	if !ok || te.Code != "local_task_store_corrupt" {
		t.Fatalf("error = %#v", err)
	}
}

func taskContextServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
}

func writePrepareJournal(t *testing.T, store *Store, journal PrepareJournal) {
	t.Helper()
	if err := os.MkdirAll(store.Layout().PrepareJournalsRoot(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.writeJSON(filepath.Join(store.Layout().PrepareJournalsRoot(), journal.ID+".json"), journal); err != nil {
		t.Fatal(err)
	}
}
