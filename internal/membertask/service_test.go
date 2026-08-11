package membertask

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

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

func TestServiceListPersistsVerificationAndMakesMissingLocalTasksReadOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tasks":[{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"both","role":"worker","attempt":2,"task_version":3,"context_version":4,"cloud_status":"assigned"}]}`))
	}))
	defer srv.Close()
	store, _ := OpenStore(t.TempDir())
	present, _ := ParseTaskKey("cloud/ws/both/worker")
	missing, _ := ParseTaskKey("cloud/ws/missing/worker")
	if err := store.SaveTask(TaskRecord{Key: present, Prepared: true}); err != nil {
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
	if presentRecord.RemoteVersion != "2:3:4" || presentRecord.LastVerifiedAt == nil || !presentRecord.LastVerifiedAt.Equal(now) {
		t.Fatalf("present record = %+v", presentRecord)
	}
	missingRecord, _, _ := store.LoadTask(missing)
	if !missingRecord.ReadOnly || missingRecord.CloudStateUnverified {
		t.Fatalf("missing record = %+v", missingRecord)
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

func testCredentials() (*provider.Credentials, error) {
	return &provider.Credentials{AccessToken: "token"}, nil
}
