package localserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"cs-cloud/internal/membertask"
	"cs-cloud/internal/provider"
)

func TestMemberTaskRouteRejectsNonLoopback(t *testing.T) {
	srv := New(WithMemberTask(memberTaskTestService(t), "private-secret"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member-tasks", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("Authorization", "Bearer private-secret")
	recorder := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestMemberTaskRouteRequiresPrivateSecret(t *testing.T) {
	srv := New(WithMemberTask(memberTaskTestService(t), "private-secret"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member-tasks", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	recorder := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestMemberTaskRouteListsTasksForAuthenticatedLoopback(t *testing.T) {
	srv := New(WithMemberTask(memberTaskTestService(t), "private-secret"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member-tasks", nil)
	req.RemoteAddr = "[::1]:1234"
	req.Header.Set("Authorization", "Bearer private-secret")
	recorder := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestMemberTaskRouteStrictlyDecodesTaskKey(t *testing.T) {
	srv := New(WithMemberTask(memberTaskTestService(t), "private-secret"))
	encoded := base64.RawURLEncoding.EncodeToString([]byte("cloud/ws/node/worker"))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/member-tasks/"+encoded, nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer private-secret")
	recorder := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestMemberTaskPrepareUsesRequestedWorkDir(t *testing.T) {
	srv := New(WithMemberTask(memberTaskTestService(t), "private-secret"))
	encoded := base64.RawURLEncoding.EncodeToString([]byte("cloud/ws/node/worker"))
	workDir := t.TempDir()
	body, _ := json.Marshal(map[string]string{"workdir": workDir})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/member-tasks/"+encoded+"/prepare", strings.NewReader(string(body)))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer private-secret")
	recorder := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data membertask.LocalTransition `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workDir, ".cs-cloud-tasks", "cloud", "ws", "node-worker")
	if response.Data.Directory != want {
		t.Fatalf("directory = %q, want %q", response.Data.Directory, want)
	}
}

func TestMemberTaskServerRejectsEmptySecretBeforeListen(t *testing.T) {
	srv := New(WithMemberTask(memberTaskTestService(t), ""))
	if err := srv.Start("127.0.0.1:0"); err == nil {
		t.Fatal("Start succeeded with empty member task secret")
	}
}

func TestMemberTaskServerRejectsNonLoopbackBindBeforeListen(t *testing.T) {
	for _, addr := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.10:0"} {
		t.Run(addr, func(t *testing.T) {
			srv := New(WithMemberTask(memberTaskTestService(t), "private-secret"))
			err := srv.Start(addr)
			if err == nil {
				_ = srv.Shutdown(context.Background())
				t.Fatal("Start succeeded with a non-loopback member task bind")
			}
			if !strings.Contains(err.Error(), "loopback") {
				t.Fatalf("Start error = %q, want loopback constraint", err)
			}
		})
	}
}

func TestMemberTaskCapabilityErrorsAreUnprocessable(t *testing.T) {
	for _, code := range []string{"material_content_unavailable", "repository_access_unavailable"} {
		t.Run(code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeMemberTaskError(recorder, &membertask.TaskError{Code: code, Message: "capability unavailable"})
			if recorder.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || body.Error.Code != code {
				t.Fatalf("body = %s, err = %v", recorder.Body.String(), err)
			}
		})
	}
}

func TestMemberTaskWorkDirErrorsUseClientStatuses(t *testing.T) {
	tests := []struct {
		code string
		want int
	}{
		{code: "invalid_workdir", want: http.StatusBadRequest},
		{code: "prepare_location_conflict", want: http.StatusConflict},
	}
	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeMemberTaskError(recorder, &membertask.TaskError{Code: test.code, Message: "workdir error"})
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, test.want, recorder.Body.String())
			}
		})
	}
}

func memberTaskTestService(t *testing.T) *membertask.Service {
	t.Helper()
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/member/tasks":
			_, _ = w.Write([]byte(`{"tasks":[{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned"}]}`))
		case "/api/member/tasks/node/worker/context":
			_, _ = w.Write([]byte(`{"cloud_instance_id":"cloud","workspace_id":"ws","node_run_id":"node","role":"worker","attempt":1,"task_version":1,"context_version":1,"cloud_status":"assigned","prepare_allowed":true,"providers":["gitea"]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(cloud.Close)
	store, err := membertask.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return membertask.NewService(store, membertask.NewCloudClient(cloud.URL, func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: "token"}, nil
	}))
}
