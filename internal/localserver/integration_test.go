package localserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/cloud"
	"cs-cloud/internal/config"
	"cs-cloud/internal/runtime"
)

// newIntegrationServer wires the FULL event stack used by csc TUI:
//
//   HTTP ingest ──▶ EventBus ──▶ RingBuffer (history)
//                       │
//                       └──▶ NotifyForwarder ──▶ mock cloud (httptest.Server)
//
//                       └──▶ TUIRegistry (owns TUI-sourced ids)
//
//                       └──▶ AgentManager (no real backend → handleProxy returns 503)
//
// The returned cloudRecorder captures every POST the mock cloud receives so
// tests can assert on the forwarded payload without reaching into the
// forwarder's internals.
func newIntegrationServer(t *testing.T, forwarderOpts forwarderOpts) (*Server, *cloudRecorder) {
	t.Helper()

	rec := &cloudRecorder{}
	cloudSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.record(r.URL.Path, body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(cloudSrv.Close)

	bus := runtime.NewEventBus()
	cfg := &config.Config{CloudBaseURL: cloudSrv.URL}
	client := cloud.NewClient(cfg)
	// Short windows so tests don't sleep for 30 seconds.
	fwd := cloud.NewNotifyForwarder(
		bus, client,
		"dev-test", "token-test", "",
		forwarderOpts.questionSec, forwarderOpts.permissionSec, forwarderOpts.idleSec,
	)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	fwd.Start(ctx)
	t.Cleanup(func() {
		// Give the forwarder a beat to drain in-flight notifications after
		// the bus stops emitting; not strictly required but reduces noise.
		time.Sleep(50 * time.Millisecond)
	})

	s := &Server{
		eventBus:    bus,
		tuiRegistry: NewTUIRegistry(),
	}
	s.manager = runtime.NewAgentManager(bus)
	s.ringBuffer = NewRingBuffer(bus)
	s.ringBuffer.Start(ctx)
	t.Cleanup(s.ringBuffer.Stop)

	// Sanity: the test never starts the registry's cleanup goroutine on
	// purpose — T9.6 depends on TTL'd-but-unpruned entries still occupying
	// a map slot at the moment of the reply.
	return s, rec
}

type forwarderOpts struct {
	questionSec   int
	permissionSec int
	idleSec       int
}

func defaultForwarderOpts() forwarderOpts {
	return forwarderOpts{questionSec: 1, permissionSec: 1, idleSec: 1}
}

// cloudRecorder is a thread-safe capture of POSTs received by the mock cloud.
type cloudRecorder struct {
	mu       sync.Mutex
	path     string
	body     []byte
	requests []recordedRequest
}

type recordedRequest struct {
	Path string
	Body []byte
}

func (r *cloudRecorder) record(path string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.path = path
	r.body = body
	r.requests = append(r.requests, recordedRequest{Path: path, Body: body})
}

func (r *cloudRecorder) lastPayload() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.body) == 0 {
		return nil
	}
	var p map[string]any
	_ = json.Unmarshal(r.body, &p)
	return p
}

func (r *cloudRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// postEvent posts a single agent event body to /api/v1/runtime/events as
// the csc TUI TypeScript client would.
func postEvent(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", bytes.NewReader([]byte(body)))
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)
	return w
}

// waitFor polls fn every 5ms until it returns true or the deadline elapses.
// Used to bridge async gaps (EventBus subscriber goroutine → ring buffer).
func waitFor(t *testing.T, deadline time.Duration, fn func() bool) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for time.Now().Before(stop) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("waitFor: condition never became true within %v", deadline)
}

// --- T9.1 ----------------------------------------------------------------
// TUI pushes permission.asked → NotifyForwarder cloud mock receives a
// POST /cloud/device/notify with type=permission (single → not batch).
func TestT9_1_PermissionAskedReachesCloud(t *testing.T) {
	s, rec := newIntegrationServer(t, defaultForwarderOpts())

	body := `{"type":"permission.asked","conversation_id":"sess-t9-1","data":{"id":"perm-t9-1","tool":{"name":"bash"}}}`
	w := postEvent(t, s, body)
	if w.Code != http.StatusNoContent {
		t.Fatalf("ingest: want 204, got %d (body=%s)", w.Code, w.Body.String())
	}

	// Forwarder buffers permission events for `permissionSec` (=1s) before flush.
	waitFor(t, 3*time.Second, func() bool { return rec.count() >= 1 })

	payload := rec.lastPayload()
	if payload == nil {
		t.Fatal("cloud received no notification")
	}
	if payload["type"] != "permission" {
		t.Errorf("cloud payload type: want permission, got %v", payload["type"])
	}
	if payload["sessionID"] != "sess-t9-1" {
		t.Errorf("cloud payload sessionID: want sess-t9-1, got %v", payload["sessionID"])
	}
}

// --- T9.2 ----------------------------------------------------------------
// TUI pushes session.idle → NotifyForwarder 30s debounce (compressed to 1s
// in test) → cloud mock receives a single idle notify, even if multiple
// idle events arrive during the window.
func TestT9_2_SessionIdleDebouncedReachesCloud(t *testing.T) {
	s, rec := newIntegrationServer(t, defaultForwarderOpts())

	// Push 3 idle events within the 1s debounce window.
	for i := 0; i < 3; i++ {
		postEvent(t, s, `{"type":"session.idle","conversation_id":"sess-t9-2"}`)
		time.Sleep(100 * time.Millisecond)
	}

	// During the window → cloud should NOT have been notified yet.
	if rec.count() != 0 {
		t.Fatalf("expected 0 notifications during debounce, got %d", rec.count())
	}

	// Past the 1s window → exactly one idle notify.
	waitFor(t, 3*time.Second, func() bool { return rec.count() >= 1 })
	if rec.count() != 1 {
		t.Fatalf("expected 1 notification after debounce, got %d", rec.count())
	}
	payload := rec.lastPayload()
	if payload["type"] != "idle" {
		t.Errorf("cloud payload type: want idle, got %v", payload["type"])
	}
	if payload["sessionID"] != "sess-t9-2" {
		t.Errorf("cloud payload sessionID: want sess-t9-2, got %v", payload["sessionID"])
	}
}

// --- T9.3 ----------------------------------------------------------------
// Cloud reply arrives at POST /permissions/{id}/reply → TUI-sourced
// dispatcher emits permission.replied → ring buffer contains it.
// This is the critical TUI-side closure of the reply loop.
func TestT9_3_ReplyFlowEmitsRepliedAndRingBufferCatches(t *testing.T) {
	s, _ := newIntegrationServer(t, defaultForwarderOpts())

	// Step 1: TUI pushes permission.asked → registry tracks it.
	postEvent(t, s, `{"type":"permission.asked","conversation_id":"sess-t9-3","data":{"id":"perm-t9-3"}}`)
	waitFor(t, time.Second, func() bool { return s.tuiRegistry.IsTUISourced("perm-t9-3") })

	// Step 2: Cloud dispatcher replies via POST /permissions/{id}/reply.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-t9-3/reply", bytes.NewReader([]byte(`{"decision":"allow"}`)))
	req.SetPathValue("id", "perm-t9-3")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("reply: want 204, got %d (body=%s)", w.Code, w.Body.String())
	}

	// Step 3: ring buffer must have caught the permission.replied event.
	waitFor(t, time.Second, func() bool {
		events := s.ringBuffer.Query(0, "sess-t9-3")
		for _, evt := range events {
			if evt.Type == "permission.replied" {
				return true
			}
		}
		return false
	})

	// Sanity: the entry is forgotten after emit, so a duplicate reply
	// would now fall through to the proxy.
	if s.tuiRegistry.IsTUISourced("perm-t9-3") {
		t.Errorf("registry: entry should be forgotten after reply emit")
	}
}

// --- T9.4 ----------------------------------------------------------------
// TUI polls GET /runtime/events → pulls the permission.replied event that
// the dispatcher emitted in T9.3.
func TestT9_4_TUIGetEventsPullsReply(t *testing.T) {
	s, _ := newIntegrationServer(t, defaultForwarderOpts())

	// Seed: TUI push + cloud reply (same as T9.3).
	postEvent(t, s, `{"type":"permission.asked","conversation_id":"sess-t9-4","data":{"id":"perm-t9-4"}}`)
	waitFor(t, time.Second, func() bool { return s.tuiRegistry.IsTUISourced("perm-t9-4") })

	replyReq := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-t9-4/reply", bytes.NewReader([]byte(`{"decision":"allow"}`)))
	replyReq.SetPathValue("id", "perm-t9-4")
	s.handlePermissionReply(httptest.NewRecorder(), replyReq)

	// Wait for the replied event to land in the ring buffer.
	waitFor(t, time.Second, func() bool {
		for _, evt := range s.ringBuffer.Query(0, "sess-t9-4") {
			if evt.Type == "permission.replied" {
				return true
			}
		}
		return false
	})

	// TUI polls.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?conversation_id=sess-t9-4", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET events: want 200, got %d", w.Code)
	}
	var out []agent.Event
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	sawAsked := false
	sawReplied := false
	for _, evt := range out {
		switch evt.Type {
		case "permission.asked":
			sawAsked = true
		case "permission.replied":
			sawReplied = true
		}
	}
	if !sawAsked {
		t.Errorf("expected permission.asked in pull, got events=%v", out)
	}
	if !sawReplied {
		t.Errorf("expected permission.replied in pull, got events=%v", out)
	}
}

// --- T9.5 ----------------------------------------------------------------
// Mixed scenario: csc-serve emits events directly on the bus (as if the
// agent backend had pushed them), TUI pushes via POST /runtime/events.
// Both must land in the ring buffer, and a single GET must return them all.
func TestT9_5_MixedTUIAndCSCServe(t *testing.T) {
	s, _ := newIntegrationServer(t, defaultForwarderOpts())

	// TUI-sourced event via HTTP.
	postEvent(t, s, `{"type":"permission.asked","conversation_id":"sess-mix","data":{"id":"perm-tui"}}`)

	// csc-serve-sourced event emitted directly on the bus.
	s.eventBus.Emit(agent.Event{
		Type:           "session.idle",
		ConversationID: "sess-mix",
		Data:           map[string]any{"emitter": "csc-serve"},
	})

	// Wait for both to land in the ring buffer.
	waitFor(t, time.Second, func() bool {
		events := s.ringBuffer.Query(0, "sess-mix")
		return len(events) >= 2
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?conversation_id=sess-mix", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET events: want 200, got %d", w.Code)
	}
	var out []agent.Event
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 events (TUI + csc-serve), got %d (%v)", len(out), out)
	}
}

// --- T9.6 ----------------------------------------------------------------
// tuiRegistry TTL expired → POST /permissions/{id}/reply falls through to
// handleProxy. In the test fixture the manager has no agent endpoint, so
// handleProxy returns 503 UNAVAILABLE. (In production csc-serve would
// receive the request and return 404 or its own response — both are
// acceptable per the design.)
func TestT9_6_ExpiredTTLFallsThroughToProxy(t *testing.T) {
	s, _ := newIntegrationServer(t, defaultForwarderOpts())
	// Short TTL so we can observe expiry without slowing the suite.
	s.tuiRegistry.ttl = 20 * time.Millisecond

	postEvent(t, s, `{"type":"permission.asked","conversation_id":"sess-t9-6","data":{"id":"perm-t9-6"}}`)
	waitFor(t, time.Second, func() bool { return s.tuiRegistry.IsTUISourced("perm-t9-6") })

	// Sleep past TTL.
	time.Sleep(40 * time.Millisecond)

	// IsTUISourced now reports false because the entry is expired.
	if s.tuiRegistry.IsTUISourced("perm-t9-6") {
		t.Fatalf("precondition: expected entry to be expired")
	}

	// Cloud reply arrives after expiry → dispatcher falls through to
	// handleProxy → 503 UNAVAILABLE (no backend).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-t9-6/reply", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("id", "perm-t9-6")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expired-TTL reply: want 503 (handleProxy fall-through), got %d (body=%s)", w.Code, w.Body.String())
	}
}
