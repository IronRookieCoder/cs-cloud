package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

func tokenProvider(accessToken string) func() (*provider.Credentials, error) {
	return func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: accessToken}, nil
	}
}

func TestClientGetWorkspaces(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != workflow.MulticaWorkspacesEndpoint {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("missing auth header")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":"ws-1","name":"Test"}]`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	wss, err := c.GetWorkspaces(context.Background())
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !called || len(wss) != 1 || wss[0].ID != "ws-1" {
		t.Fatalf("called=%v len=%d", called, len(wss))
	}
}

func TestClientStartTask(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		expectedPath := "/api/daemon/tasks/task-1/start"
		if r.URL.Path != expectedPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("missing auth header")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.StartTask(context.Background(), "task-1"); err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
	}
}

func TestClientCompleteTask(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		expectedPath := "/api/daemon/tasks/task-1/complete"
		if r.URL.Path != expectedPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if payload["output"] != "done" {
			t.Fatalf("output = %v", payload["output"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.CompleteTask(context.Background(), "task-1", "done"); err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
	}
}

func TestClientFailTask(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		expectedPath := "/api/daemon/tasks/task-1/fail"
		if r.URL.Path != expectedPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if payload["error"] != "something went wrong" {
			t.Fatalf("error = %v", payload["error"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.FailTask(context.Background(), "task-1", "something went wrong", ""); err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
	}
}

func TestClientFailTaskWithFailureReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if payload["error"] != "aborted" || payload["failure_reason"] != "cancelled" {
			t.Fatalf("unexpected body: %v", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.FailTask(context.Background(), "task-1", "aborted", "cancelled"); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestClientPostTaskMessages(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		expectedPath := "/api/daemon/tasks/task-1/messages"
		if r.URL.Path != expectedPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Messages []workflow.TaskMessage `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if len(payload.Messages) != 1 || payload.Messages[0].Seq != 1 ||
			payload.Messages[0].Type != "text" || payload.Messages[0].Content != "hello world" {
			t.Fatalf("messages = %+v", payload.Messages)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.PostTaskMessages(context.Background(), "task-1", "hello world"); err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
	}
}

func TestClientRequestReturnsErrorOnStatus(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	_, err := c.GetWorkspaces(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	var stErr *StatusError
	if !errors.As(err, &stErr) {
		t.Fatalf("expected *StatusError, got %T", err)
	}
	if stErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("StatusCode = %d", stErr.StatusCode)
	}
}

func TestClientRegisterDaemon(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != workflow.MulticaDaemonRegisterEndpoint {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer token-123" {
			t.Fatalf("missing auth header")
		}
		var req workflow.DaemonRegisterRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.WorkspaceID != "ws-1" || req.DaemonID != "dev-1" {
			t.Fatalf("unexpected body: %+v", req)
		}
		if len(req.Runtimes) != 1 || req.Runtimes[0].Type != "cs-cloud" {
			t.Fatalf("unexpected runtimes: %+v", req.Runtimes)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"runtimes":[{"id":"rt-1","workspace_id":"ws-1","provider":"cs-cloud","status":"online"}]}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	rts, err := c.RegisterDaemon(context.Background(), workflow.DaemonRegisterRequest{
		WorkspaceID: "ws-1",
		DaemonID:    "dev-1",
		Runtimes: []workflow.DaemonRuntime{
			{Name: "cs-cloud", Type: "cs-cloud", Version: "1.0.0", Status: "online"},
		},
	})
	if err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
	}
	if len(rts) != 1 || rts[0].ID != "rt-1" {
		t.Fatalf("unexpected runtimes: %+v", rts)
	}
}

func TestClientHeartbeat(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != workflow.MulticaDaemonHeartbeatEndpoint {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["runtime_id"] != "rt-1" {
			t.Fatalf("unexpected body: %v", body)
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.Heartbeat(context.Background(), "rt-1"); err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
	}
}

func TestClientHeartbeatRuntimeGone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":"runtime not found"}`))
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	err := c.Heartbeat(context.Background(), "rt-gone")
	if !errors.Is(err, ErrRuntimeGone) {
		t.Fatalf("expected ErrRuntimeGone, got %v", err)
	}
}

func TestClientCreateChatSession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		if r.URL.Path != "/api/workspaces/ws-1/api/chat/sessions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var req workflow.CreateChatSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.AgentID != "agent-1" {
			t.Fatalf("agent_id = %q", req.AgentID)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(workflow.ChatSession{ID: "chat-1", AgentID: req.AgentID, Title: req.Title})
	}))
	defer ts.Close()

	c := NewClient("", ts.URL, tokenProvider("token-123"))
	session, err := c.CreateChatSession(context.Background(), "ws-1", "agent-1", "title")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if session.ID != "chat-1" {
		t.Fatalf("session id = %q", session.ID)
	}
}

func TestClientPinTaskSession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		wantPath := fmt.Sprintf(workflow.MulticaTaskSessionEndpoint, "task-1")
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var req workflow.PinTaskSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.SessionID != "sess-1" {
			t.Fatalf("session_id = %q", req.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.PinTaskSession(context.Background(), "task-1", "sess-1", ""); err != nil {
		t.Fatalf("%v", err)
	}
}

func TestClientBindNodeRunSession(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		wantPath := fmt.Sprintf(workflow.MulticaNodeRunSessionEndpoint, "nr-1")
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q", r.URL.Path)
		}
		var req workflow.BindNodeRunSessionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if req.RuntimeID != "rt-1" || req.DeviceID != "dev-1" || req.SessionID != "sess-1" {
			t.Fatalf("unexpected body: %+v", req)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, "", tokenProvider("token-123"))
	if err := c.BindNodeRunSession(context.Background(), "nr-1", "rt-1", "dev-1", "sess-1"); err != nil {
		t.Fatalf("%v", err)
	}
}
