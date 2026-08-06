package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunTaskCompletePostsToEndpoint(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workflow/tasks/task-1/complete" {
			t.Errorf("path = %s, want /api/v1/workflow/tasks/task-1/complete", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CS_CLOUD_LOCAL_URL", srv.URL)
	t.Setenv("CS_CLOUD_TASK_ID", "task-1")

	if err := runTaskComplete([]string{"--summary", "all done"}); err != nil {
		t.Fatalf("runTaskComplete: %v", err)
	}
	if got["action"] != "complete" || got["summary"] != "all done" {
		t.Errorf("body = %+v, want action=complete summary=all done", got)
	}
}

func TestRunTaskCompleteBareNoArgs(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CS_CLOUD_LOCAL_URL", srv.URL)
	t.Setenv("CS_CLOUD_TASK_ID", "task-1")

	// Bare command — no flags. Summary is optional.
	if err := runTaskComplete(nil); err != nil {
		t.Fatalf("runTaskComplete: %v", err)
	}
	if got["action"] != "complete" {
		t.Errorf("body = %+v, want action=complete", got)
	}
}

func TestRunTaskReviewApprove(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CS_CLOUD_LOCAL_URL", srv.URL)
	t.Setenv("CS_CLOUD_TASK_ID", "task-rev")

	if err := runTaskReview([]string{"--reason", "looks good"}, "approve"); err != nil {
		t.Fatalf("runTaskReview approve: %v", err)
	}
	if got["action"] != "review" || got["decision"] != "approve" || got["reason"] != "looks good" {
		t.Errorf("body = %+v, want action=review decision=approve reason=looks good", got)
	}
}

func TestRunTaskReviewReject(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CS_CLOUD_LOCAL_URL", srv.URL)
	t.Setenv("CS_CLOUD_TASK_ID", "task-rev")

	if err := runTaskReview(nil, "reject"); err != nil {
		t.Fatalf("runTaskReview reject: %v", err)
	}
	if got["action"] != "review" || got["decision"] != "reject" {
		t.Errorf("body = %+v, want action=review decision=reject", got)
	}
}

func TestRunTaskCompleteMissingEnv(t *testing.T) {
	t.Setenv("CS_CLOUD_LOCAL_URL", "")
	t.Setenv("CS_CLOUD_TASK_ID", "")
	// The complete CLI must never surface a non-zero exit, even when task
	// context is missing: it prints guidance text instead so the agent reads
	// it and recovers (cd to the task root) instead of being derailed.
	if err := runTaskComplete(nil); err != nil {
		t.Fatalf("runTaskComplete with missing env = %v, want nil (never non-zero)", err)
	}
}

// TestPostTaskCompletion_ConflictTreatedAsAccepted verifies that a 409 from
// localserver (the task already finished — first complete succeeded, or the
// task timed out) is treated as accepted. Previously the agent retried into a
// "task not running" / "not allowed" loop until it hit agent_timeout.
func TestPostTaskCompletion_ConflictTreatedAsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"CONFLICT","message":"task task-1 is not running"}`)
	}))
	defer srv.Close()
	t.Setenv("CS_CLOUD_LOCAL_URL", srv.URL)
	t.Setenv("CS_CLOUD_TASK_ID", "task-1")

	if err := runTaskComplete([]string{"--summary", "done"}); err != nil {
		t.Fatalf("runTaskComplete on 409 = %v, want nil (treat as accepted)", err)
	}
}

// TestPostTaskCompletion_RetriesTransientFailures verifies the complete call
// survives transient failures (5xx / connection blips). It is the agent's only
// chance to signal completion, so a single transient error must not be fatal.
func TestPostTaskCompletion_RetriesTransientFailures(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway) // transient
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("CS_CLOUD_LOCAL_URL", srv.URL)
	t.Setenv("CS_CLOUD_TASK_ID", "task-1")

	if err := runTaskComplete([]string{"--summary", "done"}); err != nil {
		t.Fatalf("runTaskComplete after transient retries = %v, want nil", err)
	}
	if got := attempts.Load(); got < 3 {
		t.Errorf("attempts = %d, want >= 3 (should have retried)", got)
	}
}

// TestCompletionMessage verifies the complete/review CLI never surfaces a
// non-zero exit to the agent: success confirms delivery, and failure renders
// guidance text (cd to task root + retry) so the agent reads it and recovers
// instead of being derailed by an error exit code. The task id is supplied
// separately in the task prompt.
func TestCompletionMessage(t *testing.T) {
	if msg := completionMessage("Task completion", nil); !strings.Contains(msg, "delivered") {
		t.Fatalf("success message should confirm delivery; got %q", msg)
	}

	msg := completionMessage("Task completion", errors.New("boom: connection refused"))
	if !strings.Contains(msg, "NOT DELIVERED") {
		t.Fatalf("failure message should say not delivered; got %q", msg)
	}
	if !strings.Contains(msg, "task root") || !strings.Contains(msg, "retry") {
		t.Fatalf("failure message should guide cd-to-task-root + retry; got %q", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Fatalf("failure message should include the underlying error; got %q", msg)
	}
}
