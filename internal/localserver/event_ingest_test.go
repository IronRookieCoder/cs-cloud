package localserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/runtime"
)

func newTestServerWithBus(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		eventBus:    runtime.NewEventBus(),
		tuiRegistry: NewTUIRegistry(),
	}
	s.ringBuffer = NewRingBuffer(s.eventBus)
	s.ringBuffer.Start(context.Background())
	t.Cleanup(s.ringBuffer.Stop)
	return s
}

func TestRuntimeEventPostValid(t *testing.T) {
	s := newTestServerWithBus(t)

	body := `{"type":"session.idle","conversation_id":"sess-x","data":{"foo":"bar"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestRuntimeEventPostCamelCaseConversation(t *testing.T) {
	s := newTestServerWithBus(t)
	// csc TUI TypeScript client may send camelCase; handler should still accept it.
	body := `{"type":"session.idle","conversationID":"sess-camel"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)

	if w.Code != http.StatusNoContent {
		t.Fatalf("status: want 204, got %d", w.Code)
	}
	// Verify event landed in ring buffer with conversationID normalized.
	time.Sleep(10 * time.Millisecond) // let the bus goroutine drain
	events := s.ringBuffer.Query(0, "sess-camel")
	if len(events) != 1 {
		t.Fatalf("ring buffer should have 1 event for sess-camel, got %d", len(events))
	}
}

func TestRuntimeEventPostMissingType(t *testing.T) {
	s := newTestServerWithBus(t)
	body := `{"conversation_id":"sess-x"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing type: want 400, got %d", w.Code)
	}
}

func TestRuntimeEventPostBadJSON(t *testing.T) {
	s := newTestServerWithBus(t)
	body := `{not-json`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad json: want 400, got %d", w.Code)
	}
}

func TestRuntimeEventPostWrongMethod(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: want 405, got %d", w.Code)
	}
}

func TestRuntimeEventPostReachesRingBuffer(t *testing.T) {
	s := newTestServerWithBus(t)
	body := `{"type":"permission.asked","conversation_id":"sess-rb","data":{"id":"perm-1"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleRuntimeEventPost(w, req)

	// EventBus.Emit -> subscribe channel -> ringBuffer.Append is async.
	// Drain via Query after a short spin.
	deadline := time.Now().Add(500 * time.Millisecond)
	var events []agent.Event
	for time.Now().Before(deadline) {
		events = s.ringBuffer.Query(0, "sess-rb")
		if len(events) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(events) != 1 {
		t.Fatalf("ring buffer: want 1 event, got %d", len(events))
	}
	if events[0].Type != "permission.asked" {
		t.Errorf("type: want permission.asked, got %s", events[0].Type)
	}
}
