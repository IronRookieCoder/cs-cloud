package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGetSessionBinding(t *testing.T) {
	t.Run("200 returns binding", func(t *testing.T) {
		called := false
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.Method != http.MethodGet {
				t.Fatalf("method = %q", r.Method)
			}
			if r.URL.Path != "/api/daemon/sessions/sess-1/binding" {
				t.Fatalf("path = %q", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer token-123" {
				t.Fatalf("missing auth header")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"task_id":"task-1","task_status":"failed","node_run_id":"nr-1","node_run_status":"failed","resumable":true}`))
		}))
		defer ts.Close()

		c := NewClient(ts.URL, "", tokenProvider("token-123"))
		binding, err := c.GetSessionBinding(context.Background(), "sess-1")
		if err != nil {
			t.Fatalf("GetSessionBinding: %v", err)
		}
		if !called {
			t.Fatal("server not called")
		}
		if binding.TaskID != "task-1" {
			t.Fatalf("TaskID = %q", binding.TaskID)
		}
		if binding.TaskStatus != "failed" {
			t.Fatalf("TaskStatus = %q", binding.TaskStatus)
		}
		if binding.NodeRunID != "nr-1" {
			t.Fatalf("NodeRunID = %q", binding.NodeRunID)
		}
		if binding.NodeRunStatus != "failed" {
			t.Fatalf("NodeRunStatus = %q", binding.NodeRunStatus)
		}
		if !binding.Resumable {
			t.Fatalf("Resumable = %v", binding.Resumable)
		}
	})

	t.Run("404 returns ErrSessionNotBound", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not found"}`))
		}))
		defer ts.Close()

		c := NewClient(ts.URL, "", tokenProvider("token-123"))
		_, err := c.GetSessionBinding(context.Background(), "sess-missing")
		if !errors.Is(err, ErrSessionNotBound) {
			t.Fatalf("expected ErrSessionNotBound, got %v", err)
		}
	})
}

func TestResumeBeginTask(t *testing.T) {
	t.Run("200 success", func(t *testing.T) {
		called := false
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			called = true
			if r.Method != http.MethodPost {
				t.Fatalf("method = %q", r.Method)
			}
			if r.URL.Path != "/api/daemon/tasks/task-1/resume-begin" {
				t.Fatalf("path = %q", r.URL.Path)
			}
			if r.Header.Get("Authorization") != "Bearer token-123" {
				t.Fatalf("missing auth header")
			}
			body, _ := io.ReadAll(r.Body)
			var payload map[string]any
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("unmarshal body: %v", err)
			}
			if payload["session_id"] != "sess-1" {
				t.Fatalf("session_id = %v", payload["session_id"])
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer ts.Close()

		c := NewClient(ts.URL, "", tokenProvider("token-123"))
		if err := c.ResumeBeginTask(context.Background(), "task-1", "sess-1"); err != nil {
			t.Fatalf("ResumeBeginTask: %v", err)
		}
		if !called {
			t.Fatal("server not called")
		}
	})

	t.Run("409 returns ErrTaskNotResumable", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":"not_resumable"}`))
		}))
		defer ts.Close()

		c := NewClient(ts.URL, "", tokenProvider("token-123"))
		err := c.ResumeBeginTask(context.Background(), "task-1", "sess-1")
		if !errors.Is(err, ErrTaskNotResumable) {
			t.Fatalf("expected ErrTaskNotResumable, got %v", err)
		}
	})

	t.Run("other status returns error", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"boom"}`))
		}))
		defer ts.Close()

		c := NewClient(ts.URL, "", tokenProvider("token-123"))
		err := c.ResumeBeginTask(context.Background(), "task-1", "sess-1")
		if err == nil {
			t.Fatal("expected error")
		}
		if errors.Is(err, ErrTaskNotResumable) {
			t.Fatal("unexpected ErrTaskNotResumable")
		}
		if !strings.Contains(err.Error(), "500") {
			t.Fatalf("expected 500 in error, got %v", err)
		}
	})
}
