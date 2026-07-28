package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner"
)

// newStartedTestDriver returns a started workflow driver whose Health() passes,
// for handler tests that need to get past the Health() gate. Points at a dummy
// multica URL; CheckoutRepo does not need a real multica connection.
func newStartedTestDriver(t *testing.T) *workflowrunner.Driver {
	t.Helper()
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
	}
	d := workflowrunner.NewDriver(cfg, &workflowrunner.Dependencies{
		MulticaBaseURL: "http://localhost:1",
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	return d
}

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
// with 400. The driver is started so the Health() gate passes, isolating the
// 400 to the JSON decode failure.
func TestHandleRepoCheckout_BadJSON(t *testing.T) {
	d := newStartedTestDriver(t)
	s := New(WithWorkflow(d))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/repo/checkout", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	s.handleRepoCheckout(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (bad json)", rec.Code)
	}
}

// TestHandleRepoCheckout_MissingFields verifies that missing task_id or
// repo_url is rejected with 400. The driver is started so the Health() gate
// passes; the handler never reaches CheckoutRepo.
func TestHandleRepoCheckout_MissingFields(t *testing.T) {
	d := newStartedTestDriver(t)
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
// surfaces as a 500 CHECKOUT_FAILED. The driver is started so the Health() gate
// passes; CheckoutRepo looks up a running task and returns an error when none
// exists, without needing git or a multica connection.
func TestHandleRepoCheckout_DriverError(t *testing.T) {
	d := newStartedTestDriver(t)
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

// TestRedactCreds verifies URL-embedded basic-auth credentials are scrubbed from
// any string returned to HTTP clients. Git failure messages routinely echo the
// authed clone URL (oauth2:<PAT>@host); returning that verbatim would leak the
// GitLab PAT to whoever called /repo/checkout.
func TestRedactCreds(t *testing.T) {
	cases := map[string]string{
		"https://oauth2:glpat-abcdefghijklmnop@gitlab.example.com/group/repo.git": "https://oauth2:***@gitlab.example.com/group/repo.git",
		"fatal: unable to access 'https://oauth2:secret@gitlab/x.git/': failed":   "fatal: unable to access 'https://oauth2:***@gitlab/x.git/': failed",
		// No credentials: unchanged.
		"https://gitlab.example.com/group/repo.git": "https://gitlab.example.com/group/repo.git",
		// Bare host, no userinfo: unchanged.
		"checkout failed for gitlab.example.com": "checkout failed for gitlab.example.com",
	}
	for in, want := range cases {
		if got := redactCreds(in); got != want {
			t.Errorf("redactCreds(%q)\n  = %q\n want %q", in, got, want)
		}
	}
}
