package workflow

import (
	"context"
	"encoding/json"
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

	c := NewClient(ts.URL, tokenProvider("token-123"))
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

	c := NewClient(ts.URL, tokenProvider("token-123"))
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
		if payload["result"] != "done" {
			t.Fatalf("result = %v", payload["result"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, tokenProvider("token-123"))
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

	c := NewClient(ts.URL, tokenProvider("token-123"))
	if err := c.FailTask(context.Background(), "task-1", "something went wrong"); err != nil {
		t.Fatalf("%v", err)
	}
	if !called {
		t.Fatal("server not called")
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
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("unmarshal body: %v", err)
		}
		if payload["messages"] != "hello world" {
			t.Fatalf("messages = %v", payload["messages"])
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	c := NewClient(ts.URL, tokenProvider("token-123"))
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

	c := NewClient(ts.URL, tokenProvider("token-123"))
	_, err := c.GetWorkspaces(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
}
