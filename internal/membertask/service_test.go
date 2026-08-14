package membertask

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"cs-cloud/internal/provider"
)

func TestServiceGetAuthorityErrorsAreNotOfflineSuccess(t *testing.T) {
	for _, code := range []string{"task_not_assigned", "resource_access_denied"} {
		t.Run(code, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"denied","retryable":false}}`))
			}))
			defer srv.Close()
			store, _ := OpenStore(t.TempDir())
			key, _ := ParseTaskKey("cloud/ws/node/worker")
			if err := store.SaveTask(TaskRecord{Key: key, Prepared: true}); err != nil {
				t.Fatal(err)
			}
			_, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).Get(context.Background(), key)
			var taskErr *TaskError
			if !errors.As(err, &taskErr) || taskErr.Code != code {
				t.Fatalf("Get error = %#v, want %s", err, code)
			}
			record, _, err := store.LoadTask(key)
			if err != nil || !record.ReadOnly || record.Offline {
				t.Fatalf("record = %+v, err = %v", record, err)
			}
		})
	}
}

func TestServiceListReloadsTaskAfterAcquiringLock(t *testing.T) {
	requestStarted := make(chan struct{})
	allowResponse := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		<-allowResponse
		_, _ = w.Write([]byte(`{"tasks":[{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned"}]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	initial := TaskRecord{Key: key, Prepared: true}
	if err := store.SaveTask(initial); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	unlock := svc.lockTask(key)
	result := make(chan error, 1)
	go func() {
		_, err := svc.List(context.Background())
		result <- err
	}()
	<-requestStarted
	updated := initial
	updated.Dirty = true
	if err := store.SaveTask(updated); err != nil {
		unlock()
		t.Fatal(err)
	}
	close(allowResponse)

	select {
	case err := <-result:
		unlock()
		t.Fatalf("List completed while task lock was held: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	unlock()
	if err := <-result; err != nil {
		t.Fatalf("List: %v", err)
	}
	record, _, err := store.LoadTask(key)
	if err != nil || !record.Dirty {
		t.Fatalf("record = %+v, err = %v", record, err)
	}
}

func TestServiceListPersistsVerificationAndMakesMissingLocalTasksReadOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"both","role":"worker","attempt":2,"task_version":3,"context_version":4,"cloud_status":"assigned"}]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	present, _ := ParseTaskKey("cloud/ws/both/worker")
	missing, _ := ParseTaskKey("cloud/ws/missing/worker")
	if err := store.SaveTask(TaskRecord{Key: present, Prepared: true, ReadOnly: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(TaskRecord{Key: missing, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	svc.now = func() time.Time { return now }
	if _, err := svc.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	presentRecord, _, _ := store.LoadTask(present)
	if presentRecord.RemoteVersion != "2:3:4" || presentRecord.LastVerifiedAt == nil || !presentRecord.LastVerifiedAt.Equal(now) || presentRecord.ReadOnly {
		t.Fatalf("present record = %+v", presentRecord)
	}
	missingRecord, _, _ := store.LoadTask(missing)
	if !missingRecord.ReadOnly || missingRecord.CloudStateUnverified {
		t.Fatalf("missing record = %+v", missingRecord)
	}
}

func TestNormalizeDisplayNameBoundsUnicodeRunes(t *testing.T) {
	got := normalizeDisplayName(strings.Repeat("界", 200))
	if utf8.RuneCountInString(got) != maxDisplayNameRunes {
		t.Fatalf("display name rune count = %d, want %d", utf8.RuneCountInString(got), maxDisplayNameRunes)
	}
}

func TestServiceListMergesOnlineTasksAndLocalHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[
			{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"online","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned"},
			{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"both","role":"critic","attempt":2,"task_version":4,"context_version":5,"cloud_status":"in_progress"}
		]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	localOnly, _ := ParseTaskKey("cloud/ws/local/worker")
	both, _ := ParseTaskKey("cloud/ws/both/critic")
	if err := store.SaveTask(TaskRecord{Key: localOnly, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(TaskRecord{Key: both, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	tasks, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 3 {
		t.Fatalf("len(tasks) = %d, tasks = %+v", len(tasks), tasks)
	}
	if tasks[0].Key.String() != "cloud/ws/both/critic" || tasks[2].Key.String() != "cloud/ws/online/worker" {
		t.Fatalf("sorted keys = %s, %s, %s", tasks[0].Key.String(), tasks[1].Key.String(), tasks[2].Key.String())
	}
}

func TestServiceListUsesCloudDisplayNameAndMarksIdentityFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[
			{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"named","role":"worker","display_name":"修复登录流程","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned"},
			{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"fallback","role":"critic","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned"}
		]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	tasks, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("tasks = %+v", tasks)
	}
	byNode := map[string]Task{}
	for _, task := range tasks {
		byNode[task.Key.NodeRunID] = task
	}
	if got := byNode["named"]; got.DisplayName != "修复登录流程" || got.DisplayNameSource != DisplayNameSourceCloud {
		t.Fatalf("named task = %+v", got)
	}
	if got := byNode["fallback"]; got.DisplayName != "ws/fallback/critic" || got.DisplayNameSource != DisplayNameSourceIdentity {
		t.Fatalf("fallback task = %+v", got)
	}
}

func TestServiceListPersistsIssueMetadataForOfflineHistory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"issue-task","role":"worker","title":"实现","issue_id":"issue-1","issue_number":42,"issue_identifier":"ENG-42","issue_title":"修复登录","issue_description":"补充登录校验","workspace_slug":"engineering","task_kind":"initial","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned"}]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/issue-task/worker")
	if err := store.SaveTask(TaskRecord{Key: key, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.LoadTask(key)
	if err != nil || !found || record.IssueIdentifier != "ENG-42" || record.IssueDescription != "补充登录校验" {
		t.Fatalf("record = %+v, found=%v, err=%v", record, found, err)
	}
	offline := NewService(store, NewCloudClient("http://127.0.0.1:1", testCredentials))
	tasks, err := offline.List(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Local == nil || tasks[0].Local.IssueIdentifier != "ENG-42" {
		t.Fatalf("offline tasks = %+v, err=%v", tasks, err)
	}
}

func TestServiceListFallsBackToLocalHistoryOffline(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/local/worker")
	if err := store.SaveTask(TaskRecord{Key: key, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, NewCloudClient("http://127.0.0.1:1", testCredentials))
	tasks, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 || !tasks[0].Projection.Flags.Offline || !tasks[0].Projection.Flags.CloudStateUnverified {
		t.Fatalf("tasks = %+v", tasks)
	}
}

func TestServiceGetContextDoesNotCreateTaskDirectory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/member/tasks/node/worker/context" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`))
	}))
	defer srv.Close()
	root := t.TempDir()
	store, _ := OpenStore(root)
	svc := NewService(store, NewCloudClient(srv.URL, testCredentials))
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	task, err := svc.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task.Context == nil || task.Context.ContextVersion != 3 {
		t.Fatalf("task = %+v", task)
	}
	if _, err := os.Stat(store.Layout().TasksRoot()); !os.IsNotExist(err) {
		t.Fatalf("tasks root exists after Get: %v", err)
	}
}

func TestServiceGetRefreshesUntrackedRepositoryOutput(t *testing.T) {
	srv := taskContextServer(t, `{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`)
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	dir := store.Layout().TaskDir(key)
	repoDir := filepath.Join(dir, "repositories", "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "Test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	if err := os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "README.md"}, {"commit", "-m", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	head := strings.TrimSpace(runGitTest(t, repoDir, "rev-parse", "HEAD"))
	manifest := Manifest{SchemaVersion: SchemaVersion, Repositories: []RepositoryManifest{{Identity: "repo", RelativePath: "repositories/repo", Role: MaterialOutputWritable, BaseSHA: head, BaselineHead: head, OutputPaths: []string{"local-task-e2e.md"}}}}
	if err := store.SaveTask(TaskRecord{Key: key, Directory: dir, Prepared: true, Manifest: &manifest}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "local-task-e2e.md"), []byte("done\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	task, err := NewService(store, NewCloudClient(srv.URL, testCredentials)).Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if task.Local == nil || !task.Local.Dirty || len(task.Local.DirtyPaths) != 1 || task.Local.DirtyPaths[0] != "repositories/repo/local-task-e2e.md" {
		t.Fatalf("task = %+v", task)
	}
}

func TestServiceGetOfflineUsesPersistedDisplayName(t *testing.T) {
	store, _ := OpenStore(t.TempDir())
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	if err := store.SaveTask(TaskRecord{Key: key, DisplayName: "离线任务", DisplayNameSource: DisplayNameSourceCloud, Prepared: true}); err != nil {
		t.Fatal(err)
	}
	task, err := NewService(store, NewCloudClient("http://127.0.0.1:1", testCredentials)).Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if task.DisplayName != "离线任务" || task.DisplayNameSource != DisplayNameSourceLocal {
		t.Fatalf("task = %+v", task)
	}
}

func testCredentials() (*provider.Credentials, error) {
	return &provider.Credentials{AccessToken: "token"}, nil
}
