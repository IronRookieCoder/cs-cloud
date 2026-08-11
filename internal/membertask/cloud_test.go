package membertask

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"cs-cloud/internal/provider"
)

func TestCloudClientSendsWorkspaceHeaderForTaskRequests(t *testing.T) {
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Workspace-ID"); got != "ws" {
			t.Errorf("X-Workspace-ID = %q, want ws", got)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/member/tasks/node/worker/context":
			_, _ = w.Write([]byte(`{"ref":{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"},"attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":true}`))
		case "/api/member/tasks/node/worker/submit/preview":
			var request SubmitPreviewRequest
			_ = json.NewDecoder(r.Body).Decode(&request)
			_ = json.NewEncoder(w).Encode(Preview{ID: "preview-1", Attempt: request.Attempt, TaskVersion: request.TaskVersion, ContextVersion: request.ContextVersion, MaterialDigest: request.MaterialDigest, ContentDigest: request.ContentDigest})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := NewCloudClient(srv.URL, testCredentials)
	if _, err := client.GetContext(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, err := client.PreviewSubmit(context.Background(), key, SubmitPreviewRequest{Attempt: 1, TaskVersion: 1, ContextVersion: 1}); err != nil {
		t.Fatal(err)
	}
}

func TestCloudClientListsTasksWithBearerAuthAndRetriesReadFailure(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/member/tasks" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token-value" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tasks":[{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":2,"context_version":3,"cloud_status":"assigned"}]}`))
	}))
	defer srv.Close()
	client := NewCloudClient(srv.URL, func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: "token-value"}, nil
	})
	tasks, err := client.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(tasks) != 1 || tasks[0].Key().String() != "cloud/ws/node/worker" {
		t.Fatalf("tasks = %+v", tasks)
	}
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
}

func TestCloudClientMapsStableAPIErrorWithoutRawBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":{"code":"provider_not_supported","message":"unsupported","retryable":false},"secret":"must-not-leak"}`))
	}))
	defer srv.Close()
	client := NewCloudClient(srv.URL, func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: "secret-token"}, nil
	})
	_, err := client.List(context.Background())
	te, ok := err.(*TaskError)
	if !ok || te.Code != "provider_not_supported" || te.Cause != "" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCloudClientParsesServerNestedTaskContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/member/tasks":
			_, _ = w.Write([]byte(`{"tasks":[{"ref":{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"},"attempt":2,"task_version":3,"context_version":4,"cloud_status":"assigned"}]}`))
		case "/api/member/tasks/node/worker/context":
			_, _ = w.Write([]byte(`{"ref":{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker"},"objective":"Build the feature","attempt":2,"task_version":3,"context_version":4,"cloud_status":"assigned","prepare_allowed":true,"material_digest":"server-material","required_deliverables":[{"id":"delivery-1","title":"Result","required":true,"missing":true}],"repositories":[{"provider":"gitea","identity":"team/repo","base_ref":"refs/heads/main","base_sha":"base","prepare_allowed":true}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := NewCloudClient(srv.URL, testCredentials)
	tasks, err := client.List(context.Background())
	if err != nil || len(tasks) != 1 || tasks[0].Key().String() != "cloud/ws/node/worker" {
		t.Fatalf("tasks=%+v err=%v", tasks, err)
	}
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	contextValue, err := client.GetContext(context.Background(), key)
	if err != nil || contextValue.Goal != "Build the feature" || len(contextValue.Repositories) != 1 || contextValue.Providers[0] != "gitea" || len(contextValue.RequiredDeliverables) != 1 || contextValue.MaterialDigest != "server-material" {
		t.Fatalf("context=%+v err=%v", contextValue, err)
	}
}

func TestCloudClientParsesServerOperationContract(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"operation-1","kind":"submit","status":"accepted","attempt":1,"task_version":1,"context_version":1,"material_digest":"material","preview_digest":"preview","publish_plan":{"task_version":1,"repositories":[]},"steps":[{"repository_identity":"team/repo","expected_ref":"refs/heads/tasks/result","before_sha":"before","head_sha":"head","operation_marker":"marker","status":"not_started"}]}`))
	}))
	defer srv.Close()
	key, _ := ParseTaskKey("cloud/ws/node/worker")
	operation, err := NewCloudClient(srv.URL, testCredentials).ConfirmOperation(context.Background(), key, "operation-1")
	if err != nil {
		t.Fatalf("ConfirmOperation: %v", err)
	}
	if operation.ID != "operation-1" || len(operation.PublishSteps) != 1 || operation.PublishSteps[0].ExpectedRef != "refs/heads/tasks/result" {
		t.Fatalf("operation = %+v", operation)
	}
}
