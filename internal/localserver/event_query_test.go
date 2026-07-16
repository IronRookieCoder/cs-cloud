package localserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cs-cloud/internal/agent"
)

func TestRuntimeEventListEmpty(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var out []agent.Event
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want 0, got %d", len(out))
	}
}

func TestRuntimeEventListReturnsBufferedEvents(t *testing.T) {
	s := newTestServerWithBus(t)
	s.ringBuffer.Append(agent.Event{Type: "permission.asked", ConversationID: "sess-a"})
	s.ringBuffer.Append(agent.Event{Type: "session.idle", ConversationID: "sess-a"})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?conversation_id=sess-a", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var out []agent.Event
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("want 2, got %d", len(out))
	}
}

func TestRuntimeEventListBadSince(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?since=not-a-number", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad since: want 400, got %d", w.Code)
	}
}

func TestRuntimeEventListNegativeSince(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?since=-1", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("neg since: want 400, got %d", w.Code)
	}
}

func TestRuntimeEventListWrongMethod(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runtime/events", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method: want 405, got %d", w.Code)
	}
}

func TestRuntimeEventListNilRingBuffer(t *testing.T) {
	// Server without a ring buffer should not panic; returns empty array.
	s := &Server{eventBus: nil, tuiRegistry: NewTUIRegistry()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("nil rb: want 200, got %d", w.Code)
	}
}

func TestRuntimeEventListSinceFilter(t *testing.T) {
	s := newTestServerWithBus(t)
	s.ringBuffer.Append(agent.Event{Type: "old", ConversationID: "s"})
	// future cutoff (year 5138) filters out the buffered event
	req := httptest.NewRequest(http.MethodGet, "/api/v1/runtime/events?since=99999999999999", nil)
	w := httptest.NewRecorder()
	s.handleRuntimeEventList(w, req)
	var out []agent.Event
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out) != 0 {
		t.Errorf("future since: want 0, got %d", len(out))
	}
}
