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
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent/csc"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

// fakeAdoptBackend records the task terminal callbacks and message posts made
// by AdoptUserTurn.
type fakeAdoptBackend struct {
	mu            sync.Mutex
	completeCalls []adoptCompleteCall
	failCalls     []adoptFailCall
	messageCalls  []adoptMessageCall
}

type adoptCompleteCall struct {
	TaskID    string
	Output    string
	SessionID string
	WorkDir   string
}

type adoptFailCall struct {
	TaskID        string
	Reason        string
	FailureReason string
}

type adoptMessageCall struct {
	TaskID  string
	Content string
}

func newFakeAdoptBackend() *fakeAdoptBackend {
	return &fakeAdoptBackend{}
}

func (f *fakeAdoptBackend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/daemon/tasks/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		path := r.URL.Path
		parts := strings.Split(strings.Trim(path, "/"), "/")
		if len(parts) < 3 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		taskID := parts[len(parts)-2]
		suffix := parts[len(parts)-1]

		var bodyMap map[string]any
		_ = json.Unmarshal(body, &bodyMap)

		f.mu.Lock()
		defer f.mu.Unlock()
		switch suffix {
		case "complete":
			f.completeCalls = append(f.completeCalls, adoptCompleteCall{
				TaskID:    taskID,
				Output:    stringValue(bodyMap, "output"),
				SessionID: stringValue(bodyMap, "session_id"),
				WorkDir:   stringValue(bodyMap, "work_dir"),
			})
		case "fail":
			f.failCalls = append(f.failCalls, adoptFailCall{
				TaskID:        taskID,
				Reason:        stringValue(bodyMap, "error"),
				FailureReason: stringValue(bodyMap, "failure_reason"),
			})
		case "messages":
			content := ""
			if msgs, ok := bodyMap["messages"].([]any); ok && len(msgs) > 0 {
				if msg, ok := msgs[0].(map[string]any); ok {
					content = stringValue(msg, "content")
				}
			}
			f.messageCalls = append(f.messageCalls, adoptMessageCall{
				TaskID:  taskID,
				Content: content,
			})
		}
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (f *fakeAdoptBackend) snapshot() (complete []adoptCompleteCall, fail []adoptFailCall, messages []adoptMessageCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]adoptCompleteCall{}, f.completeCalls...),
		append([]adoptFailCall{}, f.failCalls...),
		append([]adoptMessageCall{}, f.messageCalls...)
}

func stringValue(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// fakeCSCServer serves the SSE event stream, session directory lookup, and
// message list used by AdoptUserTurn.
type fakeCSCServer struct {
	mu              sync.Mutex
	sessionDir      string
	messages        json.RawMessage
	events          []cscEvent
	subscribed      chan struct{}
	eventStatusCode int
	hangEvent       bool
	holdOpen        bool
}

type cscEvent struct {
	Name string
	Data string
}

func newFakeCSCServer(sessionDir string, messages json.RawMessage, events []cscEvent) *fakeCSCServer {
	return &fakeCSCServer{
		sessionDir: sessionDir,
		messages:   messages,
		events:     events,
	}
}

func newFakeCSCServerWithSubscribeSignal(sessionDir string, messages json.RawMessage, events []cscEvent) *fakeCSCServer {
	return &fakeCSCServer{
		sessionDir: sessionDir,
		messages:   messages,
		events:     events,
		subscribed: make(chan struct{}),
	}
}

func newFakeCSCServerWithEventFailure(statusCode int) *fakeCSCServer {
	return &fakeCSCServer{
		eventStatusCode: statusCode,
	}
}

func newFakeCSCServerWithHangingEvent() *fakeCSCServer {
	return &fakeCSCServer{
		hangEvent: true,
	}
}

// newFakeCSCServerHoldOpen serves the given events and then holds the SSE
// stream open until the client disconnects, simulating an in-flight turn.
func newFakeCSCServerHoldOpen(messages json.RawMessage, events []cscEvent) *fakeCSCServer {
	return &fakeCSCServer{
		messages: messages,
		events:   events,
		holdOpen: true,
	}
}

func (f *fakeCSCServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/event", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		statusCode := f.eventStatusCode
		subscribed := f.subscribed
		hangEvent := f.hangEvent
		f.mu.Unlock()

		if subscribed != nil {
			close(subscribed)
		}

		if hangEvent {
			<-r.Context().Done()
			return
		}

		if statusCode != 0 {
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte("event stream unavailable"))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		f.mu.Lock()
		events := append([]cscEvent{}, f.events...)
		f.mu.Unlock()
		for _, ev := range events {
			_, _ = fmt.Fprintf(w, "event: %s\n", ev.Name)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", ev.Data)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		f.mu.Lock()
		holdOpen := f.holdOpen
		f.mu.Unlock()
		if holdOpen {
			<-r.Context().Done()
		}
	})
	mux.HandleFunc("/session/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.Trim(r.URL.Path, "/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		sessionID := parts[1]
		remainder := ""
		if len(parts) > 2 {
			remainder = parts[2]
		}
		switch {
		case r.Method == http.MethodGet && remainder == "":
			f.mu.Lock()
			dir := f.sessionDir
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":        sessionID,
				"directory": dir,
			})
		case r.Method == http.MethodGet && remainder == "message":
			f.mu.Lock()
			msgs := f.messages
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(msgs)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	return mux
}

func startAdoptDriver(t *testing.T, backendURL string) *Driver {
	t.Helper()
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"csc"},
	}
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: backendURL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "tok"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start driver: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	return d
}

func TestAdoptUserTurnCompletesOnIdle(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	sessionDir := t.TempDir()
	messages := json.RawMessage(`{"messages":[{"role":"assistant","parts":[{"type":"text","text":"hello from assistant"}]}]}`)
	events := []cscEvent{
		{Name: "session.status", Data: `{"status":{"type":"busy"}}`},
		{Name: "session.result", Data: `{"subtype":"success"}`},
		{Name: "session.idle", Data: `{}`},
	}
	cscSrv := httptest.NewServer(newFakeCSCServer(sessionDir, messages, events).handler())
	defer cscSrv.Close()

	agent := csc.NewAgentWithEndpoint(cscSrv.URL)

	d := startAdoptDriver(t, backendSrv.URL)
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", agent); err != nil {
		t.Fatalf("AdoptUserTurn: %v", err)
	}

	waitFor(t, "complete callback", func() bool {
		complete, _, _ := backend.snapshot()
		return len(complete) > 0
	})

	complete, fail, messagesOut := backend.snapshot()
	if len(fail) != 0 {
		t.Fatalf("expected no fail calls, got %+v", fail)
	}
	if len(complete) != 1 {
		t.Fatalf("expected one complete call, got %d", len(complete))
	}
	if complete[0].Output != "hello from assistant" {
		t.Errorf("complete output = %q, want %q", complete[0].Output, "hello from assistant")
	}
	if complete[0].SessionID != "session-1" {
		t.Errorf("complete session_id = %q, want %q", complete[0].SessionID, "session-1")
	}
	if complete[0].WorkDir != sessionDir {
		t.Errorf("complete work_dir = %q, want %q", complete[0].WorkDir, sessionDir)
	}
	if len(messagesOut) != 1 || messagesOut[0].Content != "hello from assistant" {
		t.Errorf("message calls = %+v, want one with assistant text", messagesOut)
	}
}

func TestAdoptUserTurnFailsOnErrorEvent(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	messages := json.RawMessage(`{"messages":[]}`)
	events := []cscEvent{
		{Name: "session.status", Data: `{"status":{"type":"busy"}}`},
		{Name: "session.result", Data: `{"subtype":"error_max_turns","isError":true}`},
		{Name: "session.error", Data: `{"error":{"subtype":"error_max_turns","message":"Max turns reached"}}`},
		{Name: "session.idle", Data: `{}`},
	}
	cscSrv := httptest.NewServer(newFakeCSCServer("", messages, events).handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL)); err != nil {
		t.Fatalf("AdoptUserTurn: %v", err)
	}

	waitFor(t, "fail callback", func() bool {
		_, fail, _ := backend.snapshot()
		return len(fail) > 0
	})

	complete, fail, _ := backend.snapshot()
	if len(complete) != 0 {
		t.Fatalf("expected no complete calls, got %+v", complete)
	}
	if len(fail) != 1 {
		t.Fatalf("expected one fail call, got %d", len(fail))
	}
	if !strings.Contains(fail[0].Reason, "Max turns reached") {
		t.Errorf("fail reason = %q, want it to contain %q", fail[0].Reason, "Max turns reached")
	}
	if fail[0].FailureReason != "agent_error" {
		t.Errorf("failure_reason = %q, want %q", fail[0].FailureReason, "agent_error")
	}
}

func TestAdoptUserTurnRejectsDuplicate(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	// The first turn never ends, so the running-map slot stays occupied.
	events := []cscEvent{
		{Name: "session.status", Data: `{"status":{"type":"busy"}}`},
	}
	cscSrv := httptest.NewServer(newFakeCSCServer("", json.RawMessage(`{"messages":[]}`), events).handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL)); err != nil {
		t.Fatalf("first AdoptUserTurn: %v", err)
	}

	err := d.AdoptUserTurn(context.Background(), "task-1", "session-2", csc.NewAgentWithEndpoint(cscSrv.URL))
	if !errors.Is(err, ErrTaskAlreadyRunning) {
		t.Fatalf("second AdoptUserTurn error = %v, want ErrTaskAlreadyRunning", err)
	}

	// Wait for the first goroutine to finish (it will fail because the SSE
	// stream ends without an idle event) so the test does not close the server
	// underneath an in-flight subscribe.
	waitFor(t, "first fail callback", func() bool {
		_, fail, _ := backend.snapshot()
		return len(fail) > 0
	})
}

func TestAdoptUserTurnReleasesRunningSlot(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	messages := json.RawMessage(`{"messages":[{"role":"assistant","parts":[{"type":"text","text":"done"}]}]}`)
	events := []cscEvent{
		{Name: "session.status", Data: `{"status":{"type":"busy"}}`},
		{Name: "session.result", Data: `{"subtype":"success"}`},
		{Name: "session.idle", Data: `{}`},
	}
	cscSrv := httptest.NewServer(newFakeCSCServer("", messages, events).handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL)); err != nil {
		t.Fatalf("first AdoptUserTurn: %v", err)
	}

	waitFor(t, "first complete callback", func() bool {
		complete, _, _ := backend.snapshot()
		return len(complete) > 0
	})

	// After the first turn completes, the same taskID can be adopted again.
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-2", csc.NewAgentWithEndpoint(cscSrv.URL)); err != nil {
		t.Fatalf("second AdoptUserTurn after release: %v", err)
	}

	waitFor(t, "second complete callback", func() bool {
		complete, _, _ := backend.snapshot()
		return len(complete) > 1
	})
}

// TestAdoptUserTurnSubscribesBeforeReturn verifies that AdoptUserTurn does not
// return until the SSE subscription is established. This closes the race where
// the proxy forwards the user prompt before the driver is listening to events.
func TestAdoptUserTurnSubscribesBeforeReturn(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	fakeCSC := newFakeCSCServerWithSubscribeSignal("", json.RawMessage(`{"messages":[]}`), []cscEvent{
		{Name: "session.status", Data: `{"status":{"type":"busy"}}`},
		{Name: "session.result", Data: `{"subtype":"success"}`},
		{Name: "session.idle", Data: `{}`},
	})
	cscSrv := httptest.NewServer(fakeCSC.handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL)); err != nil {
		t.Fatalf("AdoptUserTurn: %v", err)
	}

	select {
	case <-fakeCSC.subscribed:
		// ok — subscription was established before return.
	case <-time.After(5 * time.Second):
		t.Fatal("AdoptUserTurn returned before the SSE subscription was established")
	}
}

// TestAdoptUserTurnSubscribeFailureReportsFailure verifies that a failed SSE
// subscription is reported to the backend as a failed task and the error is
// returned to the caller.
func TestAdoptUserTurnSubscribeFailureReportsFailure(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	cscSrv := httptest.NewServer(newFakeCSCServerWithEventFailure(http.StatusServiceUnavailable).handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL))
	if err == nil {
		t.Fatal("AdoptUserTurn returned nil error on subscribe failure")
	}

	waitFor(t, "fail callback", func() bool {
		_, fail, _ := backend.snapshot()
		return len(fail) > 0
	})

	_, fail, _ := backend.snapshot()
	if len(fail) != 1 {
		t.Fatalf("expected one fail call, got %d", len(fail))
	}
	if fail[0].FailureReason != "agent_error" {
		t.Errorf("failure_reason = %q, want %q", fail[0].FailureReason, "agent_error")
	}
	if !strings.Contains(fail[0].Reason, "event stream unavailable") {
		t.Errorf("fail reason = %q, want it to contain %q", fail[0].Reason, "event stream unavailable")
	}
}

// TestAdoptUserTurnSubscribeEstablishmentTimeout verifies that a hanging SSE
// subscription establishment is bounded and reported to the backend as a failed
// task. Returning an error lets the proxy forward the prompt normally.
func TestAdoptUserTurnSubscribeEstablishmentTimeout(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	cscSrv := httptest.NewServer(newFakeCSCServerWithHangingEvent().handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	d.adoptEstablishmentTimeout = 100 * time.Millisecond

	start := time.Now()
	err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("AdoptUserTurn returned nil error on establishment timeout")
	}
	if elapsed > time.Second {
		t.Fatalf("AdoptUserTurn took %s, want under 1s", elapsed)
	}

	waitFor(t, "fail callback", func() bool {
		_, fail, _ := backend.snapshot()
		return len(fail) > 0
	})

	_, fail, _ := backend.snapshot()
	if len(fail) != 1 {
		t.Fatalf("expected one fail call, got %d", len(fail))
	}
	if fail[0].FailureReason != "agent_error" {
		t.Errorf("failure_reason = %q, want %q", fail[0].FailureReason, "agent_error")
	}
}

// TestAdoptUserTurnReserveErrorsAreNotDuplicate verifies that non-duplicate
// reserve failures (e.g. driver not running) are surfaced as-is, not mapped to
// ErrTaskAlreadyRunning.
func TestAdoptUserTurnReserveErrorsAreNotDuplicate(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	cscSrv := httptest.NewServer(newFakeCSCServer("", json.RawMessage(`{"messages":[]}`), nil).handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	// Stop the driver so reserveTaskID fails the Health() check.
	if err := d.Stop(); err != nil {
		t.Fatalf("stop driver: %v", err)
	}

	err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL))
	if errors.Is(err, ErrTaskAlreadyRunning) {
		t.Fatalf("AdoptUserTurn error = %v, should not be ErrTaskAlreadyRunning", err)
	}
	if err == nil {
		t.Fatal("AdoptUserTurn returned nil error when driver was stopped")
	}
}

// TestAdoptUserTurnAbortReportsCancelled verifies that AbortTask cancels an
// adopted turn's watch and that the abort is reported with failure_reason
// "cancelled", mirroring the dispatched-run path.
func TestAdoptUserTurnAbortReportsCancelled(t *testing.T) {
	backend := newFakeAdoptBackend()
	backendSrv := httptest.NewServer(backend.handler())
	defer backendSrv.Close()

	// The turn goes busy and then stays in flight until the watch is cancelled.
	events := []cscEvent{
		{Name: "session.status", Data: `{"status":{"type":"busy"}}`},
	}
	cscSrv := httptest.NewServer(newFakeCSCServerHoldOpen(json.RawMessage(`{"messages":[]}`), events).handler())
	defer cscSrv.Close()

	d := startAdoptDriver(t, backendSrv.URL)
	if err := d.AdoptUserTurn(context.Background(), "task-1", "session-1", csc.NewAgentWithEndpoint(cscSrv.URL)); err != nil {
		t.Fatalf("AdoptUserTurn: %v", err)
	}

	if err := d.AbortTask("task-1"); err != nil {
		t.Fatalf("AbortTask: %v", err)
	}

	waitFor(t, "fail callback", func() bool {
		_, fail, _ := backend.snapshot()
		return len(fail) > 0
	})

	complete, fail, _ := backend.snapshot()
	if len(complete) != 0 {
		t.Fatalf("expected no complete calls, got %+v", complete)
	}
	if len(fail) != 1 {
		t.Fatalf("expected one fail call, got %d", len(fail))
	}
	if fail[0].FailureReason != "cancelled" {
		t.Errorf("failure_reason = %q, want %q", fail[0].FailureReason, "cancelled")
	}
}
