package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner"
)

// TestHandleRepoCheckout_NoDriver verifies the nil-driver 404 path: when the
// workflow subsystem is not registered, the endpoint reports unavailable
// rather than panicking on a nil dereference.
func TestHandleRepoCheckout_NoDriver(t *testing.T) {
	s := New() // no WithWorkflow => s.workflow == nil
	b, _ := json.Marshal(map[string]string{"task_id": "t1", "repo_url": "https://x/y.git"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/repo/checkout", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handleRepoCheckout(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (no workflow driver)", rec.Code)
	}
}

// TestHandleRepoCheckout_BadJSON verifies that a malformed body is rejected
// with 400 before the driver is touched. Uses a non-nil-but-unstarted driver:
// the handler validates before calling CheckoutRepo, so Start() is not needed.
func TestHandleRepoCheckout_BadJSON(t *testing.T) {
	d := workflowrunner.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflow(d))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repo/checkout", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	s.handleRepoCheckout(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (bad json)", rec.Code)
	}
}

// TestHandleRepoCheckout_MissingFields verifies that missing task_id or
// repo_url is rejected with 400. The handler never reaches CheckoutRepo, so an
// unstarted driver is sufficient.
func TestHandleRepoCheckout_MissingFields(t *testing.T) {
	d := workflowrunner.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflow(d))

	cases := []struct {
		name string
		body map[string]string
	}{
		{"missing task_id", map[string]string{"repo_url": "https://x/y.git"}},
		{"missing repo_url", map[string]string{"task_id": "t1"}},
		{"both missing", map[string]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b, _ := json.Marshal(c.body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/repo/checkout", bytes.NewReader(b))
			rec := httptest.NewRecorder()
			s.handleRepoCheckout(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, c.name)
			}
		})
	}
}

// TestHandleRepoCheckout_DriverError verifies that a CheckoutRepo failure
// surfaces as a 500 CHECKOUT_FAILED. Using an unstarted driver: CheckoutRepo
// looks up a running task and returns an error when none exists, without
// needing git or a multica connection.
func TestHandleRepoCheckout_DriverError(t *testing.T) {
	d := workflowrunner.NewDriver(workflow.Config{}, nil)
	s := New(WithWorkflow(d))

	b, _ := json.Marshal(map[string]string{"task_id": "no-such-task", "repo_url": "https://x/y.git"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/repo/checkout", bytes.NewReader(b))
	rec := httptest.NewRecorder()
	s.handleRepoCheckout(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (checkout failed)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "CHECKOUT_FAILED") {
		t.Fatalf("body = %s, want CHECKOUT_FAILED code", rec.Body.String())
	}
}
