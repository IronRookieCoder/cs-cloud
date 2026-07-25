package localserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPermissionListWithTUIPendingReturnsArray(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-a", "conv-1"))
	s.tuiRegistry.Register(permEvt("perm-b", "conv-2"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions", nil)
	w := httptest.NewRecorder()
	s.handlePermissionList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("pending: want 2, got %d", len(out))
	}
	seen := map[string]bool{}
	for _, entry := range out {
		// SaaS's parseIDList expects id at top level (not nested in agent.Event.data).
		if id, _ := entry["id"].(string); id != "" {
			seen[id] = true
		}
	}
	if !seen["perm-a"] || !seen["perm-b"] {
		t.Errorf("expected perm-a and perm-b at top-level id, got %v", seen)
	}
}

func TestQuestionListWithTUIPendingReturnsArray(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(questionEvt("q-a", "conv-1"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/questions", nil)
	w := httptest.NewRecorder()
	s.handleQuestionList(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200, got %d", w.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Errorf("pending: want 1, got %d", len(out))
	}
	// Question events store the device questionID under both data.id and
	// data.questionID in csc's payload; flattenEvents must surface one of
	// them at the top level so SaaS's parseIDList can decode it.
	id, _ := out[0]["id"].(string)
	if id == "" {
		t.Errorf("expected non-empty top-level id, got %v", out[0])
	}
}

func TestPermissionListEmptyFallsThroughToProxy(t *testing.T) {
	s := newTestServerWithBus(t)
	// No TUI pending — manager has no endpoint, so handleProxy returns 503.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions", nil)
	w := httptest.NewRecorder()
	s.handlePermissionList(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("fall-through: want 503, got %d", w.Code)
	}
}

func TestQuestionListEmptyFallsThroughToProxy(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/questions", nil)
	w := httptest.NewRecorder()
	s.handleQuestionList(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("fall-through: want 503, got %d", w.Code)
	}
}

func TestPermissionListWrongMethod(t *testing.T) {
	s := newTestServerWithBus(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/permissions", nil)
	w := httptest.NewRecorder()
	s.handlePermissionList(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method: want 405, got %d", w.Code)
	}
}

// Permission and question pending are distinct — having permission pending
// must NOT make GET /questions return TUI data (and vice versa).
func TestPermissionPendingDoesNotLeakIntoQuestions(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(permEvt("perm-x", "conv"))

	// GET /questions has no TUI pending → falls through to proxy → 503.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/questions", nil)
	w := httptest.NewRecorder()
	s.handleQuestionList(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("questions fall-through: want 503, got %d (body=%s)", w.Code, w.Body.String())
	}
}

func TestQuestionPendingDoesNotLeakIntoPermissions(t *testing.T) {
	s := newTestServerWithBus(t)
	s.tuiRegistry.Register(questionEvt("q-x", "conv"))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/permissions", nil)
	w := httptest.NewRecorder()
	s.handlePermissionList(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("permissions fall-through: want 503, got %d (body=%s)", w.Code, w.Body.String())
	}
}
