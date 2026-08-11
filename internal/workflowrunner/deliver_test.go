package workflowrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/workflow"
)

func TestPostTaskFact_DeliversToFactsEndpoint(t *testing.T) {
	var gotBody string
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q", r.Method)
		}
		wantPath := fmt.Sprintf(workflow.TaskFactsEndpoint, "task-1")
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", tokenProvider("tok"))
	f := OutboxFact{
		FactID:     "fact-1",
		TaskID:     "task-1",
		Kind:       "complete",
		Output:     "done",
		SessionID:  "sess-1",
		WorkDir:    "/tmp/wd",
		Decision:   "approve",
		Reason:     "lgtm",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}
	if err := c.PostTaskFact(context.Background(), f); err != nil {
		t.Fatalf("PostTaskFact: %v", err)
	}
	if !called {
		t.Fatal("facts endpoint not called")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	for _, key := range []string{
		"fact_id", "task_id", "kind", "occurred_at", "output",
		"session_id", "work_dir", "decision", "reason", "error", "failure_reason",
	} {
		if _, ok := body[key]; !ok {
			t.Errorf("missing wire key %q in body: %s", key, gotBody)
		}
	}
	if body["fact_id"] != "fact-1" || body["task_id"] != "task-1" || body["kind"] != "complete" {
		t.Fatalf("unexpected identity body: %s", gotBody)
	}
	if body["output"] != "done" || body["session_id"] != "sess-1" || body["work_dir"] != "/tmp/wd" {
		t.Fatalf("unexpected context body: %s", gotBody)
	}
	if body["decision"] != "approve" || body["reason"] != "lgtm" {
		t.Fatalf("unexpected signal body: %s", gotBody)
	}
	// Empty-string fields must still be present on the wire (no omitempty).
	if body["error"] != "" || body["failure_reason"] != "" {
		t.Fatalf("unexpected empty-string fields: %s", gotBody)
	}
}

func TestPostTaskFact_FallbackToCompleteOn404(t *testing.T) {
	factsCalled := false
	legacyCalled := false
	var legacyBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == fmt.Sprintf(workflow.TaskFactsEndpoint, "task-1") {
			factsCalled = true
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == fmt.Sprintf(workflow.TaskCompleteEndpoint, "task-1") {
			legacyCalled = true
			b, _ := io.ReadAll(r.Body)
			legacyBody = string(b)
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Fatalf("unexpected path %q", r.URL.Path)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", tokenProvider("tok"))
	f := OutboxFact{
		FactID:     "fact-1",
		TaskID:     "task-1",
		Kind:       "complete",
		Output:     "done",
		SessionID:  "sess-1",
		WorkDir:    "/tmp/wd",
		Decision:   "approve",
		Reason:     "lgtm",
		OccurredAt: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}
	if err := c.PostTaskFact(context.Background(), f); err != nil {
		t.Fatalf("PostTaskFact: %v", err)
	}
	if !factsCalled {
		t.Fatal("facts endpoint not called")
	}
	if !legacyCalled {
		t.Fatal("legacy complete endpoint not called")
	}
	if !strings.Contains(legacyBody, `"output":"done"`) {
		t.Errorf("legacy body missing output: %s", legacyBody)
	}
	if !strings.Contains(legacyBody, `"session_id":"sess-1"`) {
		t.Errorf("legacy body missing session_id: %s", legacyBody)
	}
	if !strings.Contains(legacyBody, `"work_dir":"/tmp/wd"`) {
		t.Errorf("legacy body missing work_dir: %s", legacyBody)
	}
	if !strings.Contains(legacyBody, `"decision":"approve"`) {
		t.Errorf("legacy body missing decision: %s", legacyBody)
	}
	if !strings.Contains(legacyBody, `"reason":"lgtm"`) {
		t.Errorf("legacy body missing reason: %s", legacyBody)
	}
}

func TestPostTaskFact_FallbackToFailOn404(t *testing.T) {
	factsCalled := false
	legacyCalled := false
	var legacyBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == fmt.Sprintf(workflow.TaskFactsEndpoint, "task-1") {
			factsCalled = true
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Path == fmt.Sprintf(workflow.TaskFailEndpoint, "task-1") {
			legacyCalled = true
			b, _ := io.ReadAll(r.Body)
			legacyBody = string(b)
			w.WriteHeader(http.StatusOK)
			return
		}
		t.Fatalf("unexpected path %q", r.URL.Path)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", tokenProvider("tok"))
	f := OutboxFact{
		FactID:        "fact-1",
		TaskID:        "task-1",
		Kind:          "fail",
		Error:         "it broke",
		FailureReason: "agent_error",
		OccurredAt:    time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC),
	}
	if err := c.PostTaskFact(context.Background(), f); err != nil {
		t.Fatalf("PostTaskFact: %v", err)
	}
	if !factsCalled {
		t.Fatal("facts endpoint not called")
	}
	if !legacyCalled {
		t.Fatal("legacy fail endpoint not called")
	}
	if !strings.Contains(legacyBody, `"error":"it broke"`) {
		t.Errorf("legacy body missing error: %s", legacyBody)
	}
	if !strings.Contains(legacyBody, `"failure_reason":"agent_error"`) {
		t.Errorf("legacy body missing failure_reason: %s", legacyBody)
	}
}

func TestPostTaskFact_Returns5xxError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"overloaded"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "", tokenProvider("tok"))
	f := OutboxFact{FactID: "fact-1", TaskID: "task-1", Kind: "complete", Output: "done"}
	err := c.PostTaskFact(context.Background(), f)
	if err == nil {
		t.Fatal("expected error")
	}
	var stErr *StatusError
	if !errors.As(err, &stErr) {
		t.Fatalf("expected *StatusError, got %T", err)
	}
	if stErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", stErr.StatusCode)
	}
}

func TestDeliver_LoopDeliversPendingFact(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != fmt.Sprintf(workflow.TaskFactsEndpoint, "task-1") {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		called <- struct{}{}
	}))
	defer srv.Close()

	o := newTestOutbox(t)
	f := sampleFact("fact-1")
	if err := o.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := &Driver{client: NewClient(srv.URL, "", tokenProvider("tok")), outbox: o}
	ctx, cancel := context.WithCancel(context.Background())
	go d.startOutboxDelivery(ctx)

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("server was not called")
	}
	// Give the loop time to MarkDone before cancelling.
	time.Sleep(50 * time.Millisecond)
	cancel()

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending facts, got %d", len(pending))
	}
}

func TestDeliver_5xxLeavesPending(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		called <- struct{}{}
	}))
	defer srv.Close()

	o := newTestOutbox(t)
	f := sampleFact("fact-1")
	if err := o.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := &Driver{client: NewClient(srv.URL, "", tokenProvider("tok")), outbox: o}
	d.deliverOutboxPass(context.Background())

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("server was not called")
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending fact, got %d", len(pending))
	}
	if pending[0].Attempts != 1 {
		t.Fatalf("expected attempts=1, got %d", pending[0].Attempts)
	}
}

func TestDeliver_404FallsBackToLegacy(t *testing.T) {
	factsCalled := make(chan struct{}, 1)
	legacyCalled := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == fmt.Sprintf(workflow.TaskFactsEndpoint, "task-1") {
			w.WriteHeader(http.StatusNotFound)
			factsCalled <- struct{}{}
			return
		}
		if r.URL.Path == fmt.Sprintf(workflow.TaskCompleteEndpoint, "task-1") {
			w.WriteHeader(http.StatusOK)
			legacyCalled <- struct{}{}
			return
		}
		t.Fatalf("unexpected path %q", r.URL.Path)
	}))
	defer srv.Close()

	o := newTestOutbox(t)
	f := sampleFact("fact-1")
	if err := o.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := &Driver{client: NewClient(srv.URL, "", tokenProvider("tok")), outbox: o}
	d.deliverOutboxPass(context.Background())

	select {
	case <-factsCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("facts endpoint was not called")
	}
	select {
	case <-legacyCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("legacy complete endpoint was not called")
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending facts, got %d", len(pending))
	}
}

func TestDeliver_409MarksDone(t *testing.T) {
	called := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		called <- struct{}{}
	}))
	defer srv.Close()

	o := newTestOutbox(t)
	f := sampleFact("fact-1")
	if err := o.Add(f); err != nil {
		t.Fatalf("Add: %v", err)
	}

	d := &Driver{client: NewClient(srv.URL, "", tokenProvider("tok")), outbox: o}
	d.deliverOutboxPass(context.Background())

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("server was not called")
	}

	pending, err := o.Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected 0 pending facts, got %d", len(pending))
	}
}

func TestDeliver_NoOutboxReturnsImmediately(t *testing.T) {
	d := &Driver{client: NewClient("http://localhost:1", "", tokenProvider("tok"))}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		d.startOutboxDelivery(ctx)
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("startOutboxDelivery did not return after cancel")
	}
}
