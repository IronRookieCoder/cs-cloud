package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	if err := runTaskComplete(nil); err == nil {
		t.Fatal("expected error when CS_CLOUD_LOCAL_URL is unset")
	}
}
