package localserver

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/runtime"
)

// eventCollector drains a subscription channel into a mutex-guarded slice
// so tests can read the captured events without racing the drain goroutine.
type eventCollector struct {
	mu      sync.Mutex
	events  []agent.Event
	stopped chan struct{}
}

func newEventCollector(bus *runtime.EventBus) (*eventCollector, func()) {
	ch := bus.Subscribe(nil)
	c := &eventCollector{stopped: make(chan struct{})}
	go func() {
		for {
			select {
			case evt := <-ch:
				c.mu.Lock()
				c.events = append(c.events, evt)
				c.mu.Unlock()
			case <-c.stopped:
				return
			}
		}
	}()
	return c, func() {
		close(c.stopped)
		bus.Unsubscribe(ch)
	}
}

func (c *eventCollector) snapshot() []agent.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]agent.Event, len(c.events))
	copy(out, c.events)
	return out
}

func TestPermissionReplyTUISourcedEmitsEventAnd204(t *testing.T) {
	s := newTestServerWithBus(t)
	if err := s.tuiRegistry.Register(permEvt("perm-7", "conv-1")); err != nil {
		t.Fatalf("registry: %v", err)
	}

	coll, stop := newEventCollector(s.eventBus)
	defer stop()

	body := `{"decision":"allow","reason":"looks good"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-7/reply", bytes.NewReader([]byte(body)))
	req.SetPathValue("id", "perm-7")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d (body=%s)", w.Code, w.Body.String())
	}

	// Wait for the subscriber goroutine to observe the emitted event.
	deadline := time.Now().Add(time.Second)
	var events []agent.Event
	for time.Now().Before(deadline) {
		events = coll.snapshot()
		if len(events) >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(events) != 1 {
		t.Fatalf("emitted events: want 1, got %d", len(events))
	}
	evt := events[0]
	if evt.Type != "permission.replied" {
		t.Errorf("type: want permission.replied, got %s", evt.Type)
	}
	if evt.ConversationID != "conv-1" {
		t.Errorf("conv: want conv-1, got %q", evt.ConversationID)
	}
	data, _ := evt.Data.(map[string]any)
	if data["id"] != "perm-7" {
		t.Errorf("data.id: want perm-7, got %v", data["id"])
	}
	if data["kind"] != "permission" {
		t.Errorf("data.kind: want permission, got %v", data["kind"])
	}
	if data["decision"] != "allow" {
		t.Errorf("data.decision: want allow, got %v", data["decision"])
	}

	// Registry entry should be forgotten so a duplicate reply falls through.
	if s.tuiRegistry.IsTUISourced("perm-7") {
		t.Errorf("post-reply IsTUISourced: want false (entry forgotten)")
	}
}

func TestQuestionReplyTUISourcedEmitsReplied(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(questionEvt("q-7", "conv-2"))

	coll, stop := newEventCollector(s.eventBus)
	defer stop()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/questions/q-7/reply", bytes.NewReader([]byte(`{"text":"42"}`)))
	req.SetPathValue("id", "q-7")
	w := httptest.NewRecorder()
	s.handleQuestionReply(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d", w.Code)
	}
	// Drain the subscriber goroutine.
	deadline := time.Now().Add(time.Second)
	var events []agent.Event
	for time.Now().Before(deadline) {
		events = coll.snapshot()
		if len(events) >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(events) != 1 || events[0].Type != "question.replied" {
		t.Fatalf("emitted: want 1 question.replied, got %v", events)
	}
	data, _ := events[0].Data.(map[string]any)
	if data["kind"] != "question" {
		t.Errorf("kind: want question, got %v", data["kind"])
	}
	if data["text"] != "42" {
		t.Errorf("text: want 42, got %v", data["text"])
	}
}

func TestQuestionRejectTUISourcedEmitsRejected(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(questionEvt("q-8", "conv-3"))

	coll, stop := newEventCollector(s.eventBus)
	defer stop()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/questions/q-8/reject", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("id", "q-8")
	w := httptest.NewRecorder()
	s.handleQuestionReject(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d", w.Code)
	}
	deadline := time.Now().Add(time.Second)
	var events []agent.Event
	for time.Now().Before(deadline) {
		events = coll.snapshot()
		if len(events) >= 1 {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if len(events) != 1 || events[0].Type != "question.rejected" {
		t.Fatalf("emitted: want 1 question.rejected, got %v", events)
	}
}

func TestReplyUnknownIDFallsThroughToProxy(t *testing.T) {
	s := newTestServerWithBus(t)
	// Manager is real but has no agent endpoint, so handleProxy returns 503.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/nope/reply", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("id", "nope")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("fall-through: want 503, got %d", w.Code)
	}
}

func TestReplyBadJSON(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-9", "conv"))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-9/reply", bytes.NewReader([]byte(`{not-json`)))
	req.SetPathValue("id", "perm-9")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: want 400, got %d", w.Code)
	}
}

func TestReplyEmptyBody(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-10", "conv"))

	coll, stop := newEventCollector(s.eventBus)
	defer stop()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-10/reply", nil)
	req.SetPathValue("id", "perm-10")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)

	// Empty body is valid — the user may have nothing to add beyond "replied".
	if w.Code != http.StatusNoContent {
		t.Errorf("empty body: want 204, got %d", w.Code)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if len(coll.snapshot()) >= 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Errorf("emitted: want 1, got %d", len(coll.snapshot()))
}

func TestReplyWrongMethod(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-11", "conv"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions/perm-11/reply", nil)
	req.SetPathValue("id", "perm-11")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method: want 405, got %d", w.Code)
	}
}

// Test that a duplicate reply after the first one falls through to the proxy
// (and thus 503 in the test setup, but in production would 404 on csc-serve).
// This guards the Forget-on-emit behaviour.
func TestReplyDuplicateFallsThroughAfterForget(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-12", "conv"))

	// First reply: TUI path.
	req1 := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-12/reply", bytes.NewReader([]byte(`{}`)))
	req1.SetPathValue("id", "perm-12")
	w1 := httptest.NewRecorder()
	s.handlePermissionReply(w1, req1)
	if w1.Code != http.StatusNoContent {
		t.Fatalf("first reply: want 204, got %d", w1.Code)
	}

	// Second reply: registry entry forgotten → falls through to proxy → 503.
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-12/reply", bytes.NewReader([]byte(`{}`)))
	req2.SetPathValue("id", "perm-12")
	w2 := httptest.NewRecorder()
	s.handlePermissionReply(w2, req2)
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("second reply: want 503 (fall-through), got %d", w2.Code)
	}
}

// Body-too-large: a pathological payload (>64KB) is rejected with 400.
func TestReplyBodyTooLarge(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-13", "conv"))

	big := bytes.Repeat([]byte("a"), maxReplyBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions/perm-13/reply", bytes.NewReader(big))
	req.SetPathValue("id", "perm-13")
	w := httptest.NewRecorder()
	s.handlePermissionReply(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("oversize body: want 400, got %d", w.Code)
	}
}