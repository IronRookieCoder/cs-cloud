package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestServerWithDispatcher() *Server {
	srv := New(WithVersion("test"))
	d := NewCommandDispatcher(nil, nil)
	mock := &mockReconnecter{}
	d.BindTunnel(mock)
	d.BindRestarter(func() { time.Sleep(2 * time.Second) })
	srv.SetDispatcher(d)
	return srv
}

func TestCommandDispatchInvalidMethod(t *testing.T) {
	srv := newTestServerWithDispatcher()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/commands", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Logf("body: %s", w.Body.String())
	}
}

func TestCommandDispatchMissingCommandID(t *testing.T) {
	srv := newTestServerWithDispatcher()

	body, _ := json.Marshal(commandRequest{Type: "restart"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestCommandDispatchInvalidType(t *testing.T) {
	srv := newTestServerWithDispatcher()

	body, _ := json.Marshal(commandRequest{CommandID: "cmd-1", Type: "invalid"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestCommandDispatchInvalidJSON(t *testing.T) {
	srv := newTestServerWithDispatcher()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader([]byte("not json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestCommandDispatchSuccess(t *testing.T) {
	srv := newTestServerWithDispatcher()

	body, _ := json.Marshal(commandRequest{CommandID: "cmd-ok", Type: "reconnect"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp envelope
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.OK {
		t.Errorf("ok=%v, want true", resp.OK)
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", resp.Data)
	}
	if data["command_id"] != "cmd-ok" {
		t.Errorf("command_id=%v", data["command_id"])
	}
	if data["status"] != "accepted" {
		t.Errorf("status=%v", data["status"])
	}
}

func TestCommandStatusMissingID(t *testing.T) {
	srv := newTestServerWithDispatcher()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/commands/status", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", w.Code)
	}
}

func TestCommandStatusNotFound(t *testing.T) {
	srv := newTestServerWithDispatcher()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/commands/status?command_id=nope", nil)
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", w.Code)
	}
}

func TestCommandStatusAfterDispatch(t *testing.T) {
	srv := newTestServerWithDispatcher()

	body, _ := json.Marshal(commandRequest{CommandID: "cmd-status", Type: "reconnect"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	time.Sleep(200 * time.Millisecond)

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/commands/status?command_id=cmd-status", nil)
	w2 := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w2.Code, w2.Body.String())
	}

	var resp envelope
	json.NewDecoder(w2.Body).Decode(&resp)
	data, _ := resp.Data.(map[string]any)
	if data["status"] != "completed" {
		t.Errorf("status=%v, want completed", data["status"])
	}
}

func TestCommandDispatchNoDispatcher(t *testing.T) {
	srv := New(WithVersion("test"))

	body, _ := json.Marshal(commandRequest{CommandID: "cmd-nodispatcher", Type: "reconnect"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", w.Code)
	}
}

func TestCommandDispatchRestartAgentSuccess(t *testing.T) {
	srv := newTestServerWithDispatcher()

	body, _ := json.Marshal(commandRequest{CommandID: "cmd-ra-ok", Type: "restart-agent"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/commands", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.http.Handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}

	var resp envelope
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.OK {
		t.Errorf("ok=%v, want true", resp.OK)
	}

	data, ok := resp.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type=%T", resp.Data)
	}
	if data["command_id"] != "cmd-ra-ok" {
		t.Errorf("command_id=%v", data["command_id"])
	}
	if data["status"] != "accepted" {
		t.Errorf("status=%v", data["status"])
	}
}

