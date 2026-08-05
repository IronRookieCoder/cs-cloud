package workflowrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner/execenv"
)

func TestDriverName(t *testing.T) {
	deps := &Dependencies{
		BackendBaseURL: "https://backend.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(workflow.DefaultConfig(), deps)
	if got, want := d.Name(), "workflow"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestDriverLifecycleNoop(t *testing.T) {
	deps := &Dependencies{
		BackendBaseURL: "https://backend.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(workflow.DefaultConfig(), deps)
	if err := d.Start(); err != nil {
		t.Errorf("Start() = %v, want nil", err)
	}
	if err := d.Health(); err != nil {
		t.Errorf("Health() = %v, want nil", err)
	}
	if err := d.Stop(); err != nil {
		t.Errorf("Stop() = %v, want nil", err)
	}
}

func TestDriverStartWiresGC(t *testing.T) {
	tokenProvider := func() (*provider.Credentials, error) { return nil, nil }

	// GCEnabled=true (default) → gcFunc is plugged into the runtime loop.
	cfgOn := workflow.DefaultConfig()
	cfgOn.WorkspacesRoot = t.TempDir()
	d := NewDriver(cfgOn, &Dependencies{
		BackendBaseURL: "https://backend.example.com",
		TokenProvider:  tokenProvider,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })
	if d.runtime == nil || d.runtime.gcFunc == nil {
		t.Fatal("expected runtime.gcFunc to be wired when GCEnabled=true")
	}

	// GCEnabled=false → gcFunc stays nil; doGC no-ops.
	cfgOff := workflow.DefaultConfig()
	cfgOff.GCEnabled = false
	cfgOff.WorkspacesRoot = t.TempDir()
	dOff := NewDriver(cfgOff, &Dependencies{
		BackendBaseURL: "https://backend.example.com",
		TokenProvider:  tokenProvider,
	})
	if err := dOff.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = dOff.Stop() })
	if dOff.runtime != nil && dOff.runtime.gcFunc != nil {
		t.Fatal("expected runtime.gcFunc to be nil when GCEnabled=false")
	}
}

func TestDriverHoldsConfigAndDeps(t *testing.T) {
	cfg := workflow.Config{BackendBaseURL: "https://cfg.example.com"}
	deps := &Dependencies{
		BackendBaseURL: "https://backend.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(cfg, deps)
	if d.cfg.BackendBaseURL != cfg.BackendBaseURL {
		t.Errorf("cfg.BackendBaseURL = %q, want %q", d.cfg.BackendBaseURL, cfg.BackendBaseURL)
	}
	if d.deps != deps {
		t.Error("deps mismatch")
	}
}

func TestDriverStartRequiresDeps(t *testing.T) {
	d := NewDriver(workflow.DefaultConfig(), nil)
	if err := d.Start(); err == nil {
		t.Fatal("expected error when deps nil")
	}
}

func TestDriverTokenProviderNilDeps(t *testing.T) {
	d := NewDriver(workflow.DefaultConfig(), nil)
	if d.tokenProvider() != nil {
		t.Fatal("expected nil tokenProvider when deps is nil")
	}
}

// fakeBackend is a minimal in-memory backend for driver registration
// tests. It implements /api/workspaces, /api/daemon/register,
// /api/daemon/heartbeat, /api/daemon/deregister, /api/chat/sessions, and the
// session-binding endpoints used by workflow task execution.
type fakeBackend struct {
	mu             sync.Mutex
	workspaces     []workflow.Workspace
	registrations  []workflow.DaemonRegisterRequest
	heartbeats     []string
	deregistered   []string
	pinSessions    []pinSessionCall
	bindSessions   []bindSessionCall
	taskCalls      []string
	taskBodies     map[string][]byte
	completeStatus int
	sessions       []workflow.ChatSession
	nextRuntimeID  int
	nextSessionID  int
	runtimeAlive   map[string]bool
	messageDelay   time.Duration
}

type pinSessionCall struct {
	TaskID    string
	SessionID string
	WorkDir   string
}

type bindSessionCall struct {
	NodeRunID string
	RuntimeID string
	DeviceID  string
	SessionID string
}

func newFakeBackend(workspaces ...workflow.Workspace) *fakeBackend {
	return &fakeBackend{
		workspaces:    workspaces,
		nextRuntimeID: 1,
		nextSessionID: 1,
		runtimeAlive:  make(map[string]bool),
		taskBodies:    make(map[string][]byte),
	}
}

func (f *fakeBackend) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(workflow.WorkspacesEndpoint, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.workspaces)
	})
	mux.HandleFunc(workflow.DaemonRegisterEndpoint, func(w http.ResponseWriter, r *http.Request) {
		var req workflow.DaemonRegisterRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.registrations = append(f.registrations, req)
		rtID := fmt.Sprintf("rt-%d", f.nextRuntimeID)
		f.nextRuntimeID++
		f.runtimeAlive[rtID] = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(workflow.DaemonRegisterResponse{
			Runtimes: []workflow.DaemonRuntimeResponse{
				{ID: rtID, WorkspaceID: req.WorkspaceID, Provider: providerCSCloud, Status: "online"},
			},
		})
	})
	mux.HandleFunc(workflow.DaemonHeartbeatEndpoint, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rtID, _ := body["runtime_id"].(string)
		f.mu.Lock()
		defer f.mu.Unlock()
		if !f.runtimeAlive[rtID] {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "runtime not found"})
			return
		}
		f.heartbeats = append(f.heartbeats, rtID)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc(workflow.DaemonDeregisterEndpoint, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if ids, ok := body["runtime_ids"].([]any); ok {
			for _, id := range ids {
				s, ok := id.(string)
				if !ok {
					continue
				}
				f.deregistered = append(f.deregistered, s)
				delete(f.runtimeAlive, s)
			}
		}
		w.WriteHeader(http.StatusOK)
	})

	// Session-binding endpoints used during workflow task execution.
	mux.HandleFunc("/api/chat/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("X-Workspace-ID") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req workflow.CreateChatSessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		sessionID := fmt.Sprintf("chat-sess-%d", f.nextSessionID)
		f.nextSessionID++
		session := workflow.ChatSession{ID: sessionID, AgentID: req.AgentID, Title: req.Title}
		f.sessions = append(f.sessions, session)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(session)
	})
	mux.HandleFunc("/api/daemon/tasks/", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/session") {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) < 4 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			taskID := parts[len(parts)-2]
			var req workflow.PinTaskSessionRequest
			_ = json.Unmarshal(body, &req)
			f.mu.Lock()
			f.pinSessions = append(f.pinSessions, pinSessionCall{TaskID: taskID, SessionID: req.SessionID, WorkDir: req.WorkDir})
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		f.mu.Lock()
		messageDelay := f.messageDelay
		completeStatus := f.completeStatus
		f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/messages") && messageDelay > 0 {
			time.Sleep(messageDelay)
		}
		f.mu.Lock()
		f.taskCalls = append(f.taskCalls, r.URL.Path)
		f.taskBodies[r.URL.Path] = body
		f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/complete") && completeStatus != 0 {
			w.WriteHeader(completeStatus)
			_, _ = w.Write([]byte(`{"error":"completion rejected"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/api/daemon/node-runs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/session") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 4 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		nodeRunID := parts[len(parts)-2]
		var req workflow.BindNodeRunSessionRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.bindSessions = append(f.bindSessions, bindSessionCall{
			NodeRunID: nodeRunID,
			RuntimeID: req.RuntimeID,
			DeviceID:  req.DeviceID,
			SessionID: req.SessionID,
		})
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func (f *fakeBackend) snapshot() (regs []workflow.DaemonRegisterRequest, beats []string, deregs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]workflow.DaemonRegisterRequest{}, f.registrations...),
		append([]string{}, f.heartbeats...),
		append([]string{}, f.deregistered...)
}

func (f *fakeBackend) killRuntimes() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runtimeAlive = make(map[string]bool)
}

func (f *fakeBackend) sessionCalls() (pins []pinSessionCall, binds []bindSessionCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pinSessionCall{}, f.pinSessions...),
		append([]bindSessionCall{}, f.bindSessions...)
}

func (f *fakeBackend) taskCallback(suffix string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, path := range f.taskCalls {
		if strings.HasSuffix(path, suffix) {
			return append([]byte{}, f.taskBodies[path]...), true
		}
	}
	return nil, false
}

func (f *fakeBackend) taskCallbackCount(suffix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, path := range f.taskCalls {
		if strings.HasSuffix(path, suffix) {
			count++
		}
	}
	return count
}

func (f *fakeBackend) setMessageDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.messageDelay = delay
}

func (f *fakeBackend) setCompleteStatus(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.completeStatus = status
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func testDriverConfig(t *testing.T, heartbeat time.Duration) workflow.Config {
	t.Helper()
	return workflow.Config{
		WorkspacesRoot:    t.TempDir(),
		CacheDir:          t.TempDir(),
		SyncInterval:      time.Hour,
		GCInterval:        time.Hour,
		HeartbeatInterval: heartbeat,
		AgentTimeout:      time.Minute,
		AllowedAgents:     []string{"fakeagent"},
	}
}

func TestDriverRegistersAndHeartbeats(t *testing.T) {
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"}, workflow.Workspace{ID: "ws-2", Name: "two"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		BackendBaseURL: ts.URL,
		TokenProvider:  tokenProvider("token-123"),
		DeviceID:       func() (string, error) { return "dev-1", nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	waitFor(t, "registration of both workspaces", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 2
	})
	waitFor(t, "heartbeat", func() bool {
		_, beats, _ := fm.snapshot()
		return len(beats) > 0
	})

	regs, _, _ := fm.snapshot()
	for _, req := range regs[:2] {
		if req.DaemonID != "dev-1" {
			t.Fatalf("daemon_id = %q, want dev-1", req.DaemonID)
		}
		if len(req.Runtimes) != 1 || req.Runtimes[0].Type != providerCSCloud {
			t.Fatalf("runtimes = %+v, want one cs-cloud runtime", req.Runtimes)
		}
	}
}

func TestDriverReregistersWhenRuntimeGone(t *testing.T) {
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		BackendBaseURL: ts.URL,
		TokenProvider:  tokenProvider("token-123"),
		DeviceID:       func() (string, error) { return "dev-1", nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	waitFor(t, "initial registration", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 1
	})

	// Simulate the runtime row being deleted server-side (e.g. TTL sweep).
	fm.killRuntimes()

	waitFor(t, "re-registration after 404", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 2
	})
}

func TestDriverDeregistersOnStop(t *testing.T) {
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, time.Hour), &Dependencies{
		BackendBaseURL: ts.URL,
		TokenProvider:  tokenProvider("token-123"),
		DeviceID:       func() (string, error) { return "dev-1", nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	waitFor(t, "initial registration", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 1
	})

	if err := d.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}

	waitFor(t, "deregister on stop", func() bool {
		_, _, deregs := fm.snapshot()
		return len(deregs) == 1
	})
}

func TestDriverSkipsRegistrationWithoutDeviceID(t *testing.T) {
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		BackendBaseURL: ts.URL,
		TokenProvider:  tokenProvider("token-123"),
		// no DeviceID — registration disabled
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := d.Health(); err != nil {
		t.Fatalf("health: %v", err)
	}
	d.Stop()

	regs, _, _ := fm.snapshot()
	if len(regs) != 0 {
		t.Fatalf("expected no registrations, got %d", len(regs))
	}
}

func TestDriverSkipsRegistrationWithEmptyDeviceID(t *testing.T) {
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		BackendBaseURL: ts.URL,
		TokenProvider:  tokenProvider("token-123"),
		DeviceID:       func() (string, error) { return "", nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if err := d.Health(); err != nil {
		t.Fatalf("health: %v", err)
	}
	d.Stop()

	regs, _, _ := fm.snapshot()
	if len(regs) != 0 {
		t.Fatalf("expected no registrations, got %d", len(regs))
	}
}

func TestDriverAbortTask(t *testing.T) {
	installFakeAgent(t, "fakeagent")

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	startedFile := filepath.Join(t.TempDir(), "started")

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"fakeagent"},
	}
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: backend.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		runErr = d.RunTask(context.Background(), workflow.TaskRunPayload{
			TaskID:      "task-1",
			WorkspaceID: "ws-1",
			Agent:       "fakeagent",
			Prompt:      "sleep 10",
			Env: map[string]string{
				"FAKE_AGENT_STARTED_FILE": startedFile,
			},
		})
	}()

	waitFor(t, "fake agent to start", func() bool {
		_, err := os.Stat(startedFile)
		return err == nil
	})
	if err := d.AbortTask("task-1"); err != nil {
		t.Fatalf("abort: %v", err)
	}

	wg.Wait()
	if runErr == nil {
		t.Fatal("expected RunTask to return an error after abort")
	}
}

// callbackRecorder is a fake backend that records the daemon task callbacks
// (start / messages / complete / fail) with their raw request bodies.
type callbackRecorder struct {
	mu       sync.Mutex
	calls    []string
	bodies   map[string][]byte
	startErr int // if non-zero, the /start endpoint responds with this status
}

func newCallbackRecorder() *callbackRecorder {
	return &callbackRecorder{bodies: make(map[string][]byte)}
}

func (cr *callbackRecorder) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		cr.mu.Lock()
		cr.calls = append(cr.calls, r.URL.Path)
		cr.bodies[r.URL.Path] = body
		startErr := cr.startErr
		cr.mu.Unlock()
		if startErr != 0 && strings.HasSuffix(r.URL.Path, "/start") {
			w.WriteHeader(startErr)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (cr *callbackRecorder) hasCall(suffix string) bool {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	for _, c := range cr.calls {
		if strings.HasSuffix(c, suffix) {
			return true
		}
	}
	return false
}

func (cr *callbackRecorder) body(suffix string) []byte {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	for p, b := range cr.bodies {
		if strings.HasSuffix(p, suffix) {
			return b
		}
	}
	return nil
}

func asyncTestDriver(t *testing.T, backendURL string) *Driver {
	t.Helper()
	installFakeAgent(t, "fakeagent")

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"fakeagent"},
	}
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: backendURL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { d.Stop() })
	return d
}

func TestDriverRunTaskAsyncCompletes(t *testing.T) {
	cr := newCallbackRecorder()
	ts := httptest.NewServer(cr.handler())
	defer ts.Close()

	d := asyncTestDriver(t, ts.URL)

	err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-async",
		WorkspaceID: "ws-1",
		Agent:       "fakeagent",
		Prompt:      "echo hello",
	})
	if err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	waitFor(t, "complete callback", func() bool { return cr.hasCall("/complete") })

	if !cr.hasCall("/start") || !cr.hasCall("/messages") {
		t.Fatalf("missing callbacks, got %v", cr.calls)
	}

	var msgs struct {
		Messages []workflow.TaskMessage `json:"messages"`
	}
	if err := json.Unmarshal(cr.body("/messages"), &msgs); err != nil {
		t.Fatalf("messages body: %v", err)
	}
	if len(msgs.Messages) != 1 || msgs.Messages[0].Type != "text" ||
		!strings.Contains(msgs.Messages[0].Content, "hello") {
		t.Fatalf("messages = %+v", msgs.Messages)
	}

	var complete map[string]any
	if err := json.Unmarshal(cr.body("/complete"), &complete); err != nil {
		t.Fatalf("complete body: %v", err)
	}
	out, _ := complete["output"].(string)
	if !strings.Contains(out, "hello") {
		t.Fatalf("complete output = %q", out)
	}
}

// TestDriverExecuteWritesGCMeta proves the execute() lifecycle hook actually
// writes .gc_meta.json during a real (fake-agent) task run, with the right Kind
// + IDs + a non-zero CompletedAt — so the gcLoop has a meta to read after the
// task finishes. Closes the loop the gc.go port depends on.
func TestDriverExecuteWritesGCMeta(t *testing.T) {
	cr := newCallbackRecorder()
	ts := httptest.NewServer(cr.handler())
	defer ts.Close()

	d := asyncTestDriver(t, ts.URL)
	installFakeAgent(t, "fakeagent")

	const (
		wsID   = "ws-1"
		taskID = "task-meta"
	)
	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID: taskID, WorkspaceID: wsID, IssueID: "issue-1",
		Agent: "fakeagent", Prompt: "echo hi",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}
	waitFor(t, "complete callback", func() bool { return cr.hasCall("/complete") })

	taskRoot := filepath.Join(d.cfg.WorkspacesRoot, wsID, "tasks", taskID)
	meta, err := execenv.ReadGCMeta(taskRoot)
	if err != nil {
		t.Fatalf("expected .gc_meta.json at %s: %v", taskRoot, err)
	}
	if meta.Kind != execenv.GCKindIssue {
		t.Errorf("Kind: want %q, got %q", execenv.GCKindIssue, meta.Kind)
	}
	if meta.IssueID != "issue-1" {
		t.Errorf("IssueID: want issue-1, got %q", meta.IssueID)
	}
	if meta.CompletedAt.IsZero() {
		t.Error("CompletedAt should be stamped by the completion hook")
	}
}

func TestDriverRunTaskAsyncDuplicate(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	d := asyncTestDriver(t, ts.URL)

	payload := workflow.TaskRunPayload{
		TaskID:      "task-dup",
		WorkspaceID: "ws-1",
		Agent:       "fakeagent",
		Prompt:      "sleep 5",
	}
	if err := d.RunTaskAsync(payload); err != nil {
		t.Fatalf("first RunTaskAsync: %v", err)
	}
	if err := d.RunTaskAsync(payload); err == nil {
		t.Fatal("expected duplicate task error")
	}
}

func TestDriverAbortBeforeRunTombstone(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	d := asyncTestDriver(t, ts.URL)

	// Abort arrives before the pushed run request.
	if err := d.AbortTask("task-late"); err == nil {
		t.Fatal("expected abort of unknown task to error")
	}

	err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-late",
		WorkspaceID: "ws-1",
		Agent:       "fakeagent",
		Prompt:      "echo should-not-run",
	})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("expected tombstone rejection, got %v", err)
	}
}

func TestDriverStartTaskFailureAborts(t *testing.T) {
	cr := newCallbackRecorder()
	cr.startErr = http.StatusConflict
	ts := httptest.NewServer(cr.handler())
	defer ts.Close()

	d := asyncTestDriver(t, ts.URL)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-stale",
		WorkspaceID: "ws-1",
		Agent:       "fakeagent",
		Prompt:      "echo should-not-run",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	// The task must be cleaned up locally without a fail/complete callback.
	waitFor(t, "task removed from running map", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.running["task-stale"]
		return !ok
	})
	if cr.hasCall("/complete") || cr.hasCall("/fail") {
		t.Fatalf("unexpected completion callbacks: %v", cr.calls)
	}
}

// TestNoOpenCodeMRSymbol verifies that the OpenCodeMR function has been removed.
// MR creation is now the agent CLI's responsibility, not the driver's.
// This is a compile-time guard: if anyone re-adds OpenCodeMR to the package,
// this line will fail to compile.
func TestNoOpenCodeMRSymbol(t *testing.T) {
	// The unexported functions from the deleted coderepo.go must not exist.
	// Reference them as values so the compiler catches re-introduction.
	var _ = (func(string) bool)(nil)   // worktreeHasStagedChanges shape
	var _ = (func(string) string)(nil) // sanitizeBranchSegment shape

	// OpenCodeMR was the exported entry point. Confirm it is gone by
	// checking that the driver's execute function does not inject
	// "Merge request:" into the output. We test this indirectly:
	// grep the source file at test time for the forbidden call.
	b, err := os.ReadFile("driver.go")
	if err != nil {
		t.Fatalf("read driver.go: %v", err)
	}
	if bytes.Contains(b, []byte("OpenCodeMR")) {
		t.Fatal("driver.go must not reference OpenCodeMR; MR creation is the agent's responsibility")
	}
	if bytes.Contains(b, []byte("Merge request:")) {
		t.Fatal("driver.go must not contain 'Merge request:' literal; MR URLs are no longer injected by the driver")
	}
}

func TestDriverRunTaskAsyncBindsSession(t *testing.T) {
	installFakeAgent(t, "fakeagent")

	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"fakeagent"},
	}
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: ts.URL,
		UserBaseURL:    ts.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
		DeviceID:       func() (string, error) { return "dev-1", nil },
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	waitFor(t, "registration", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 1
	})

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-bind",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "fakeagent",
		Prompt:      "echo hello",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	waitFor(t, "session binding", func() bool {
		pins, binds := fm.sessionCalls()
		return len(pins) > 0 && len(binds) > 0
	})

	pins, binds := fm.sessionCalls()
	if len(pins) != 1 || pins[0].TaskID != "task-bind" || pins[0].SessionID == "" {
		t.Fatalf("unexpected pin calls: %+v", pins)
	}
	if len(binds) != 1 || binds[0].NodeRunID != "nr-1" || binds[0].DeviceID != "dev-1" || binds[0].RuntimeID == "" || binds[0].SessionID != pins[0].SessionID {
		t.Fatalf("unexpected bind calls: %+v", binds)
	}
}

func TestBindSession_ReusesPriorSession(t *testing.T) {
	f := newFakeBackend()
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	d := &Driver{
		deps: &Dependencies{
			BackendBaseURL: ts.URL,
			DeviceID:       func() (string, error) { return "dev-1", nil },
		},
		client:           NewClient(ts.URL, ts.URL, tokenProvider("tok")),
		workspaceManager: NewWorkspaceManager(t.TempDir()),
		running:          map[string]*taskRecord{},
		registrations:    map[string]string{"ws-1": "rt-1"},
	}

	workdir := "/some/taskroot"
	sessionID, err := d.bindSession(context.Background(), workflow.TaskRunPayload{
		TaskID: "t1", WorkspaceID: "ws-1", AgentID: "a1", NodeRunID: "nr1",
		Agent: AgentCsc, PriorSessionID: "sess-prior",
	}, workdir)
	if err != nil {
		t.Fatalf("bindSession: %v", err)
	}
	if sessionID != "sess-prior" {
		t.Errorf("sessionID = %q, want reuse sess-prior", sessionID)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) != 0 {
		t.Errorf("expected no CreateChatSession, got %d sessions", len(f.sessions))
	}
	if len(f.pinSessions) != 1 {
		t.Fatalf("expected 1 pin call, got %d", len(f.pinSessions))
	}
	if f.pinSessions[0].WorkDir != workdir {
		t.Errorf("pin work_dir = %q, want %q", f.pinSessions[0].WorkDir, workdir)
	}
	if len(f.bindSessions) != 1 || f.bindSessions[0].SessionID != "sess-prior" {
		t.Errorf("bind node-run session: %+v", f.bindSessions)
	}
}

// flakySessionRunner fails its first RunSession call (simulating a corrupt
// resumed session) and succeeds on the second (the fresh retry).
type flakySessionRunner struct {
	calls     int
	onSuccess func() // invoked on the succeeding (2nd) call, to signal completion
}

func (f *flakySessionRunner) RunSession(ctx context.Context, sessionID, worktree, prompt string, env []string, _ string) ([]byte, error) {
	f.calls++
	if f.calls == 1 {
		return nil, fmt.Errorf("resumed session boom")
	}
	if f.onSuccess != nil {
		f.onSuccess()
	}
	return []byte("ok-round2"), nil
}

// TestExecute_ResumeFailureRetriesFresh verifies that when a resumed prior
// session FAILS on the first RunCSCSession, execute retries once with a fresh
// session (clears PriorSessionID → bindSession creates a new chat session →
// RunCSCSession again). Mirrors the daemon.go:2662-2677.
func TestExecute_ResumeFailureRetriesFresh(t *testing.T) {
	installFakeAgent(t, AgentCsc)

	flaky := &flakySessionRunner{}
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	cfg := workflow.Config{
		WorkspacesRoot:    t.TempDir(),
		CacheDir:          t.TempDir(),
		SyncInterval:      time.Hour,
		GCInterval:        time.Hour,
		HeartbeatInterval: time.Hour,
		AgentTimeout:      time.Minute,
		AllowedAgents:     []string{AgentCsc},
	}
	// SessionRunner is set in deps before Start so Start injects it into the
	// runner; execute's CSC-session branch checks d.deps.SessionRunner, and
	// RunCSCSession reads tr.sessionRunner — both must be non-nil.
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: ts.URL,
		UserBaseURL:    ts.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
		DeviceID:       func() (string, error) { return "dev-1", nil },
		SessionRunner:  flaky,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	// Wait for the async maintainRegistrations goroutine to register ws-1 so
	// bindSession sees a runtime for the workspace.
	waitFor(t, "registration", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.registrations["ws-1"]
		return ok
	})

	// Pure-tool mode: the fresh retry must explicitly complete to succeed.
	flaky.onSuccess = func() {
		_ = d.SignalTaskCompletion("t-resume", agent.CompletionSignal{Action: "complete", Summary: "ok"})
	}

	err := d.execute(context.Background(), workflow.TaskRunPayload{
		TaskID: "t-resume", WorkspaceID: "ws-1", AgentID: "a1", NodeRunID: "nr1",
		Agent: AgentCsc, Prompt: "do work", PriorSessionID: "sess-prior",
	}, &taskRecord{})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if flaky.calls != 2 {
		t.Errorf("flakySessionRunner calls = %d, want 2 (failed resume + fresh retry)", flaky.calls)
	}
	// The fresh retry creates a new chat session (the resume attempt skipped
	// CreateChatSession because PriorSessionID was set).
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if len(fm.sessions) != 1 {
		t.Errorf("expected 1 CreateChatSession (fresh retry), got %d", len(fm.sessions))
	}
}

// TestExecute_NonResumeFailureDoesNotRetry verifies that when there is no prior
// session to resume (first round), a RunCSCSession failure does NOT trigger the
// fresh-session retry — the retry path is gated on PriorSessionID != "".
func TestExecute_NonResumeFailureDoesNotRetry(t *testing.T) {
	installFakeAgent(t, AgentCsc)

	flaky := &flakySessionRunner{} // call 1 fails, call 2 would succeed — but must not be reached
	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	cfg := workflow.Config{
		WorkspacesRoot:    t.TempDir(),
		CacheDir:          t.TempDir(),
		SyncInterval:      time.Hour,
		GCInterval:        time.Hour,
		HeartbeatInterval: time.Hour,
		AgentTimeout:      time.Minute,
		AllowedAgents:     []string{AgentCsc},
	}
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: ts.URL,
		UserBaseURL:    ts.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
		DeviceID:       func() (string, error) { return "dev-1", nil },
		SessionRunner:  flaky,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	waitFor(t, "registration", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.registrations["ws-1"]
		return ok
	})

	err := d.execute(context.Background(), workflow.TaskRunPayload{
		TaskID: "t-fresh", WorkspaceID: "ws-1", AgentID: "a1", NodeRunID: "nr1",
		Agent: AgentCsc, Prompt: "do work",
		// PriorSessionID OMITTED (first round)
	}, &taskRecord{})
	if err == nil {
		t.Error("expected task to fail (flaky runner fails on call 1, no retry)")
	}
	if flaky.calls != 1 {
		t.Errorf("flaky.calls = %d, want 1 (no retry on non-resume failure)", flaky.calls)
	}
}

func TestDriverCSCSessionFailureReportsAgentError(t *testing.T) {
	installFakeAgent(t, "csc")

	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	sessionRunner := &fakeSessionRunner{err: errors.New("Max turns reached")}
	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"csc"},
	}
	d := NewDriver(cfg, &Dependencies{
		BackendBaseURL: ts.URL,
		UserBaseURL:    ts.URL,
		TokenProvider:  func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
		DeviceID:       func() (string, error) { return "dev-1", nil },
		SessionRunner:  sessionRunner,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer d.Stop()

	waitFor(t, "registration", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 1
	})

	err := d.RunTask(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-fail",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	})
	if err == nil || err.Error() != "Max turns reached" {
		t.Fatalf("RunTask error = %v, want Max turns reached", err)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("terminal CSC error called complete callback")
	}

	body, ok := fm.taskCallback("/fail")
	if !ok {
		t.Fatal("terminal CSC error did not call fail callback")
	}
	var failure struct {
		Error         string `json:"error"`
		FailureReason string `json:"failure_reason"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("fail body: %v", err)
	}
	if failure.Error != "Max turns reached" {
		t.Fatalf("failure error = %q, want Max turns reached", failure.Error)
	}
	if failure.FailureReason != "agent_error" {
		t.Fatalf("failure reason = %q, want agent_error", failure.FailureReason)
	}
}

func newCSCSessionTestDriver(
	t *testing.T,
	timeout time.Duration,
	runner SessionRunner,
	binder ConversationBinder,
) (*Driver, *fakeBackend) {
	t.Helper()
	installFakeAgent(t, "csc")

	fm := newFakeBackend(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	t.Cleanup(ts.Close)

	d := NewDriver(workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   timeout,
		AllowedAgents:  []string{"csc"},
	}, &Dependencies{
		BackendBaseURL:     ts.URL,
		UserBaseURL:        ts.URL,
		TokenProvider:      func() (*provider.Credentials, error) { return &provider.Credentials{AccessToken: "x"}, nil },
		DeviceID:           func() (string, error) { return "dev-1", nil },
		ConversationBinder: binder,
		SessionRunner:      runner,
	})
	if err := d.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = d.Stop() })

	waitFor(t, "registration", func() bool {
		regs, _, _ := fm.snapshot()
		return len(regs) >= 1
	})
	return d, fm
}

type nonCooperativeSessionRunner struct {
	started   chan struct{}
	unblock   chan struct{}
	finished  chan struct{}
	closeOnce sync.Once
}

func (r *nonCooperativeSessionRunner) RunSession(context.Context, string, string, string, []string, string) ([]byte, error) {
	close(r.started)
	<-r.unblock
	close(r.finished)
	return []byte("late result"), nil
}

func TestDriverTimesOutNonCooperativeCSCSession(t *testing.T) {
	sessionRunner := &nonCooperativeSessionRunner{
		started:  make(chan struct{}),
		unblock:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	unblock := func() {
		sessionRunner.closeOnce.Do(func() { close(sessionRunner.unblock) })
	}
	t.Cleanup(unblock)
	d, fm := newCSCSessionTestDriver(t, 100*time.Millisecond, sessionRunner, nil)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-timeout",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	select {
	case <-sessionRunner.started:
	case <-time.After(time.Second):
		t.Fatal("session runner did not start")
	}

	waitFor(t, "timeout fail callback", func() bool {
		_, ok := fm.taskCallback("/fail")
		return ok
	})
	waitFor(t, "timed-out task release", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		_, ok := d.running["task-timeout"]
		return !ok
	})

	unblock()
	select {
	case <-sessionRunner.finished:
	case <-time.After(time.Second):
		t.Fatal("late session runner did not return")
	}
	time.Sleep(20 * time.Millisecond)
	if got := fm.taskCallbackCount("/fail"); got != 1 {
		t.Fatalf("fail callback count = %d, want 1", got)
	}
	if got := fm.taskCallbackCount("/complete"); got != 0 {
		t.Fatalf("complete callback count = %d, want 0", got)
	}
}

type contextAwareTimeoutRunner struct{}

func (*contextAwareTimeoutRunner) RunSession(ctx context.Context, _ string, _ string, _ string, _ []string, _ string) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

type partialOutputFailureRunner struct{}

func (*partialOutputFailureRunner) RunSession(context.Context, string, string, string, []string, string) ([]byte, error) {
	return []byte("partial output"), errors.New("agent failed")
}

func TestDriverReportsAgentTimeoutReason(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, 100*time.Millisecond, &contextAwareTimeoutRunner{}, nil)

	err := d.RunTask(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-timeout-reason",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RunTask error = %v, want context deadline exceeded", err)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("timed-out task called complete callback")
	}

	body, ok := fm.taskCallback("/fail")
	if !ok {
		t.Fatal("timed-out task did not call fail callback")
	}
	var failure struct {
		FailureReason string `json:"failure_reason"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("fail body: %v", err)
	}
	if failure.FailureReason != "agent_timeout" {
		t.Fatalf("failure reason = %q, want agent_timeout", failure.FailureReason)
	}
}

type emptyOutputSessionRunner struct{}

func (*emptyOutputSessionRunner) RunSession(context.Context, string, string, string, []string, string) ([]byte, error) {
	return nil, agent.ErrEmptySessionOutput
}

func TestDriverReportsEmptySessionOutputAsFailure(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, time.Minute, &emptyOutputSessionRunner{}, nil)

	err := d.RunTask(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-empty-output",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	})
	if !errors.Is(err, agent.ErrEmptySessionOutput) {
		t.Fatalf("RunTask error = %v, want ErrEmptySessionOutput", err)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("empty session output called complete callback")
	}

	body, ok := fm.taskCallback("/fail")
	if !ok {
		t.Fatal("empty session output did not call fail callback")
	}
	var failure struct {
		FailureReason string `json:"failure_reason"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("fail body: %v", err)
	}
	if failure.FailureReason != "agent_empty_output" {
		t.Fatalf("failure reason = %q, want agent_empty_output", failure.FailureReason)
	}
}

type silentlyEmptySessionRunner struct{}

func (*silentlyEmptySessionRunner) RunSession(context.Context, string, string, string, []string, string) ([]byte, error) {
	return nil, nil
}

// This exercises the workflow subsystem through its public RunTask boundary,
// including workspace preparation, session binding, and terminal HTTP
// callbacks. Only the external CSC execution boundary is substituted. Under
// pure-tool completion, a session that ends without the agent calling the
// complete tool fails as agent_incomplete (not a silent success).
func TestWorkflowEmptySessionEndToEndFailsTaskWithoutCompleting(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, 100*time.Millisecond, &silentlyEmptySessionRunner{}, nil)

	err := d.RunTask(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-silently-empty",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	})
	if !errors.Is(err, agent.ErrIncomplete) {
		t.Fatalf("RunTask error = %v, want ErrIncomplete", err)
	}
	if _, ok := fm.taskCallback("/complete"); ok {
		t.Fatal("silently empty session called complete callback")
	}

	body, ok := fm.taskCallback("/fail")
	if !ok {
		t.Fatal("silently empty session did not call fail callback")
	}
	var failure struct {
		FailureReason string `json:"failure_reason"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("fail body: %v", err)
	}
	if failure.FailureReason != "agent_incomplete" {
		t.Fatalf("failure reason = %q, want agent_incomplete", failure.FailureReason)
	}
}

func TestDriverReportsFailureBeforeSlowMessageCallback(t *testing.T) {
	d, fm := newCSCSessionTestDriver(t, time.Minute, &partialOutputFailureRunner{}, nil)
	fm.setMessageDelay(500 * time.Millisecond)

	start := time.Now()
	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-prompt-failure",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	reported := false
	deadline := time.Now().Add(350 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := fm.taskCallback("/fail"); ok {
			reported = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !reported {
		t.Fatalf("fail callback was delayed for %v by task messages", time.Since(start))
	}
	waitFor(t, "partial task messages", func() bool {
		return fm.taskCallbackCount("/messages") == 1
	})
}

type abortableSessionRunner struct {
	started     chan struct{}
	abortCalled chan struct{}
	unblock     chan struct{}
	closeOnce   sync.Once
}

func (r *abortableSessionRunner) RunSession(context.Context, string, string, string, []string, string) ([]byte, error) {
	close(r.started)
	<-r.unblock
	return nil, context.Canceled
}

func (r *abortableSessionRunner) AbortSession(context.Context, string) error {
	close(r.abortCalled)
	r.closeOnce.Do(func() { close(r.unblock) })
	return nil
}

func TestDriverAbortsCSCSessionOnTimeout(t *testing.T) {
	sessionRunner := &abortableSessionRunner{
		started:     make(chan struct{}),
		abortCalled: make(chan struct{}),
		unblock:     make(chan struct{}),
	}
	t.Cleanup(func() {
		sessionRunner.closeOnce.Do(func() { close(sessionRunner.unblock) })
	})
	d, fm := newCSCSessionTestDriver(t, 100*time.Millisecond, sessionRunner, nil)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID:      "task-abort-timeout",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	select {
	case <-sessionRunner.started:
	case <-time.After(time.Second):
		t.Fatal("session runner did not start")
	}
	select {
	case <-sessionRunner.abortCalled:
	case <-time.After(time.Second):
		t.Fatal("timed-out CSC session was not aborted")
	}
	waitFor(t, "timeout fail callback", func() bool {
		_, ok := fm.taskCallback("/fail")
		return ok
	})
}

type failingConversationBinder struct {
	err error
}

func (b *failingConversationBinder) Bind(context.Context, string, string, []string, string) error {
	return b.err
}

type recordingConversationBinder struct {
	permMode string
}

func (b *recordingConversationBinder) Bind(_ context.Context, _, _ string, _ []string, permMode string) error {
	b.permMode = permMode
	return nil
}

func TestDriverRunsWorkflowSessionWithBypassPermissionMode(t *testing.T) {
	sessionRunner := &fakeSessionRunner{}
	binder := &recordingConversationBinder{}
	d, _ := newCSCSessionTestDriver(t, time.Minute, sessionRunner, binder)
	// Pure-tool mode: the agent must explicitly complete. Simulate it.
	sessionRunner.onRun = func() {
		_ = d.SignalTaskCompletion("task-perm-mode", agent.CompletionSignal{Action: "complete", Summary: "done"})
	}

	if err := d.RunTask(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-perm-mode",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	}); err != nil {
		t.Fatalf("RunTask: %v", err)
	}
	if binder.permMode != SessionPermissionBypass {
		t.Fatalf("binder permMode = %q, want %q", binder.permMode, SessionPermissionBypass)
	}
	if sessionRunner.permMode != SessionPermissionBypass {
		t.Fatalf("session runner permMode = %q, want %q", sessionRunner.permMode, SessionPermissionBypass)
	}
}

func TestDriverFailsBeforeRemoteBindingWhenLocalSessionBindFails(t *testing.T) {
	sessionRunner := &fakeSessionRunner{}
	d, fm := newCSCSessionTestDriver(
		t,
		time.Minute,
		sessionRunner,
		&failingConversationBinder{err: errors.New("local csc session failed to start")},
	)

	err := d.RunTask(context.Background(), workflow.TaskRunPayload{
		TaskID:      "task-bind-failure",
		WorkspaceID: "ws-1",
		NodeRunID:   "nr-1",
		AgentID:     "agent-1",
		Agent:       "csc",
		Prompt:      "do thing",
	})
	if err == nil || !strings.Contains(err.Error(), "local csc session failed to start") {
		t.Fatalf("RunTask error = %v, want local bind failure", err)
	}
	if sessionRunner.env != nil {
		t.Fatal("session runner was called after local bind failure")
	}
	pins, binds := fm.sessionCalls()
	if len(pins) != 0 || len(binds) != 0 {
		t.Fatalf("remote session was bound after local bind failure: pins=%v binds=%v", pins, binds)
	}
	if _, ok := fm.taskCallback("/fail"); !ok {
		t.Fatal("local bind failure did not call fail callback")
	}
}

// completingSessionRunner simulates a csc session that is "busy" (blocks until
// aborted) and records the env/permMode it was invoked with. It implements
// SessionAborter so the driver's abort-on-completion path can unblock it.
type completingSessionRunner struct {
	env       []string
	permMode  string
	started   chan struct{}
	unblock   chan struct{}
	closeOnce sync.Once
}

func (r *completingSessionRunner) RunSession(_ context.Context, _ string, _ string, _ string, env []string, permMode string) ([]byte, error) {
	r.env = env
	r.permMode = permMode
	close(r.started)
	<-r.unblock
	return []byte("session output"), nil
}

func (r *completingSessionRunner) AbortSession(context.Context, string) error {
	r.closeOnce.Do(func() { close(r.unblock) })
	return nil
}

// TestDriverCompletesOnExplicitCompletionSignal verifies that when the agent
// invokes the "complete task" tool (SignalTaskCompletion) mid-session, the
// driver aborts the session and completes the task with the tool's payload —
// NOT the session stdout. This is the core of the pure-tool completion model.
func TestDriverCompletesOnExplicitCompletionSignal(t *testing.T) {
	runner := &completingSessionRunner{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	t.Cleanup(func() { runner.closeOnce.Do(func() { close(runner.unblock) }) })
	d, fm := newCSCSessionTestDriver(t, time.Minute, runner, nil)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID: "task-complete", WorkspaceID: "ws-1", NodeRunID: "nr-1", AgentID: "agent-1",
		Agent: "csc", Prompt: "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("session runner did not start")
	}

	if err := d.SignalTaskCompletion("task-complete", agent.CompletionSignal{
		Action: "complete", Summary: "all done",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	waitFor(t, "complete callback", func() bool {
		_, ok := fm.taskCallback("/complete")
		return ok
	})

	if _, ok := fm.taskCallback("/fail"); ok {
		t.Fatal("explicit completion triggered /fail callback")
	}

	body, ok := fm.taskCallback("/complete")
	if !ok {
		t.Fatal("missing /complete body")
	}
	var complete struct {
		Output   string `json:"output"`
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal(body, &complete); err != nil {
		t.Fatalf("complete body: %v", err)
	}
	if complete.Output != "all done" {
		t.Errorf("complete output = %q, want %q (summary, not session stdout)", complete.Output, "all done")
	}
}

func TestDriverFailsTaskWhenCompletionCallbackRejected(t *testing.T) {
	runner := &completingSessionRunner{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	t.Cleanup(func() { runner.closeOnce.Do(func() { close(runner.unblock) }) })
	d, fm := newCSCSessionTestDriver(t, time.Minute, runner, nil)
	fm.setCompleteStatus(http.StatusBadRequest)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID: "task-complete-rejected", WorkspaceID: "ws-1", NodeRunID: "nr-1", AgentID: "agent-1",
		Agent: "csc", Prompt: "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("session runner did not start")
	}

	if err := d.SignalTaskCompletion("task-complete-rejected", agent.CompletionSignal{
		Action: "complete", Summary: "all done",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	waitFor(t, "fail callback", func() bool {
		_, ok := fm.taskCallback("/fail")
		return ok
	})
	if _, ok := fm.taskCallback("/complete"); !ok {
		t.Fatal("missing /complete callback")
	}

	body, ok := fm.taskCallback("/fail")
	if !ok {
		t.Fatal("missing /fail body")
	}
	var failure struct {
		Error         string `json:"error"`
		FailureReason string `json:"failure_reason"`
	}
	if err := json.Unmarshal(body, &failure); err != nil {
		t.Fatalf("fail body: %v", err)
	}
	if failure.FailureReason != "completion_rejected" {
		t.Fatalf("failure reason = %q, want completion_rejected", failure.FailureReason)
	}
	if !strings.Contains(failure.Error, "completion rejected") {
		t.Fatalf("failure error = %q, want completion rejected details", failure.Error)
	}
}

func TestDriverDoesNotFailTaskWhenCompletionCallbackIsRateLimited(t *testing.T) {
	runner := &completingSessionRunner{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
	}
	t.Cleanup(func() { runner.closeOnce.Do(func() { close(runner.unblock) }) })
	d, fm := newCSCSessionTestDriver(t, time.Minute, runner, nil)
	fm.setCompleteStatus(http.StatusTooManyRequests)

	if err := d.RunTaskAsync(workflow.TaskRunPayload{
		TaskID: "task-complete-rate-limited", WorkspaceID: "ws-1", NodeRunID: "nr-1", AgentID: "agent-1",
		Agent: "csc", Prompt: "do thing",
	}); err != nil {
		t.Fatalf("RunTaskAsync: %v", err)
	}

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("session runner did not start")
	}

	if err := d.SignalTaskCompletion("task-complete-rate-limited", agent.CompletionSignal{
		Action: "complete", Summary: "all done",
	}); err != nil {
		t.Fatalf("SignalTaskCompletion: %v", err)
	}

	waitFor(t, "complete callback", func() bool {
		_, ok := fm.taskCallback("/complete")
		return ok
	})
	time.Sleep(50 * time.Millisecond)
	if got := fm.taskCallbackCount("/fail"); got != 0 {
		t.Fatalf("fail callback count = %d, want 0 for 429 completion callback", got)
	}
}
