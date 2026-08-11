package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestReportToServer_SendsRequiredHeaders verifies the report request carries
// every header multica's middleware chain requires. The workspace membership
// middleware (RequireWorkspaceMember) rejects the request with 400
// "workspace_id or workspace_slug is required" when X-Workspace-ID is missing
// — which orphaned an already-opened PR in production. CS_CLOUD_WORKSPACE_ID is
// always present in the task env, so it must be forwarded as X-Workspace-ID.
func TestReportToServer_SendsRequiredHeaders(t *testing.T) {
	var (
		gotWorkspace, gotAgent, gotTask, gotAuth, gotBody string
		gotMethod                                         string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotWorkspace = r.Header.Get("X-Workspace-ID")
		gotAgent = r.Header.Get("X-Agent-ID")
		gotTask = r.Header.Get("X-Task-ID")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	endpoint := srv.URL + "/api/node-runs/nr-1/deliverables/del-2/submit"
	if err := reportToServer(context.Background(), srv.URL, "tok", endpoint, "https://pr", "ws-123", "agent-1", "task-9", "pull_request_url"); err != nil {
		t.Fatalf("reportToServer: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotWorkspace != "ws-123" {
		t.Errorf("X-Workspace-ID = %q, want ws-123 (without it multica returns 400)", gotWorkspace)
	}
	if gotAgent != "agent-1" {
		t.Errorf("X-Agent-ID = %q, want agent-1", gotAgent)
	}
	if gotTask != "task-9" {
		t.Errorf("X-Task-ID = %q, want task-9", gotTask)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body unmarshal: %v (body=%q)", err, gotBody)
	}
	if body["pull_request_url"] != "https://pr" {
		t.Errorf("body pull_request_url = %q, want https://pr", body["pull_request_url"])
	}
}

// TestReportToServer_OmitsEmptyWorkspaceHeader confirms an empty workspaceID
// does not send the header at all. Checking presence (not Header.Get, which
// conflates "absent" and "present-but-empty") guards against a regression to
// sending an empty-valued X-Workspace-ID header.
func TestReportToServer_OmitsEmptyWorkspaceHeader(t *testing.T) {
	var workspaceHeaderPresent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, workspaceHeaderPresent = r.Header[http.CanonicalHeaderKey("X-Workspace-ID")]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := reportToServer(context.Background(), srv.URL, "tok", srv.URL+"/x", "https://pr", "", "a", "t", "pull_request_url"); err != nil {
		t.Fatalf("reportToServer: %v", err)
	}
	if workspaceHeaderPresent {
		t.Error("X-Workspace-ID must not be sent when workspaceID is empty")
	}
}

// TestReportDeliverablePR_BuildsUnifiedEndpoint verifies the wrapper targets
// multica's unified submit route, the same path the code-MR flow uses.
func TestReportDeliverablePR_BuildsUnifiedEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := reportDeliverablePR(context.Background(), srv.URL, "tok", "nr-1", "del-2", "https://pr", "ws-1", "a", "t"); err != nil {
		t.Fatalf("reportDeliverablePR: %v", err)
	}
	if want := "/api/node-runs/nr-1/deliverables/del-2/submit"; gotPath != want {
		t.Errorf("endpoint path = %q, want %q", gotPath, want)
	}
}

// TestResolveSubmitTarget_FallsBackWhenNoReports verifies the hardcoded
// endpoint + body field are used when multica did not send per-deliverable
// Report contracts (backward compat with older multica deployments).
func TestResolveSubmitTarget_FallsBackWhenNoReports(t *testing.T) {
	t.Setenv("CS_CLOUD_DELIVERABLE_REPORTS", "")
	endpoint, bodyField := resolveSubmitTarget("del-2", "http://srv", "nr-1")
	if want := "http://srv/api/node-runs/nr-1/deliverables/del-2/submit"; endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
	if bodyField != "pull_request_url" {
		t.Fatalf("bodyField = %q, want pull_request_url", bodyField)
	}
}

// TestResolveSubmitTarget_UsesReportWhenPresent verifies that when multica
// sends a per-deliverable Report (R4), cs-cloud honors its endpoint path and
// body field instead of the hardcoded defaults.
func TestResolveSubmitTarget_UsesReportWhenPresent(t *testing.T) {
	t.Setenv("CS_CLOUD_DELIVERABLE_REPORTS", `[{"deliverable_id":"del-2","endpoint":"/api/v2/deliverables/del-2/submit","body_field":"merge_request_url"}]`)
	endpoint, bodyField := resolveSubmitTarget("del-2", "http://srv", "nr-1")
	if want := "http://srv/api/v2/deliverables/del-2/submit"; endpoint != want {
		t.Fatalf("endpoint = %q, want %q (Report.Endpoint must override the hardcoded path)", endpoint, want)
	}
	if bodyField != "merge_request_url" {
		t.Fatalf("bodyField = %q, want merge_request_url", bodyField)
	}
}

func TestResolveSubmitTarget_IgnoresReportsForOtherDeliverables(t *testing.T) {
	t.Setenv("CS_CLOUD_DELIVERABLE_REPORTS", `[{"deliverable_id":"del-9","endpoint":"/x","body_field":"y"}]`)
	endpoint, bodyField := resolveSubmitTarget("del-2", "http://srv", "nr-1")
	if want := "http://srv/api/node-runs/nr-1/deliverables/del-2/submit"; endpoint != want {
		t.Fatalf("endpoint = %q, want fallback (no matching report)", endpoint)
	}
	if bodyField != "pull_request_url" {
		t.Fatalf("bodyField = %q, want pull_request_url fallback", bodyField)
	}
}

// TestReportToServer_UsesBodyField verifies the body field name is parameterized
// (R4) so a Report.BodyField like "merge_request_url" is honored.
func TestReportToServer_UsesBodyField(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := reportToServer(context.Background(), srv.URL, "tok", srv.URL+"/x", "https://pr", "ws", "a", "t", "merge_request_url"); err != nil {
		t.Fatalf("reportToServer: %v", err)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body unmarshal: %v (body=%q)", err, gotBody)
	}
	if body["merge_request_url"] != "https://pr" {
		t.Fatalf("body = %q, want merge_request_url:https://pr", gotBody)
	}
	if _, ok := body["pull_request_url"]; ok {
		t.Fatalf("body should not contain pull_request_url when bodyField differs: %q", gotBody)
	}
}
