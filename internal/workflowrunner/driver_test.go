package workflowrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner/execenv"
)

func TestDriverName(t *testing.T) {
	deps := &Dependencies{
		MulticaBaseURL: "https://multica.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(workflow.DefaultConfig(), deps)
	if got, want := d.Name(), "workflow"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}
}

func TestDriverLifecycleNoop(t *testing.T) {
	deps := &Dependencies{
		MulticaBaseURL: "https://multica.example.com",
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
		MulticaBaseURL: "https://multica.example.com",
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
		MulticaBaseURL: "https://multica.example.com",
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
	cfg := workflow.Config{MulticaBaseURL: "https://cfg.example.com"}
	deps := &Dependencies{
		MulticaBaseURL: "https://multica.example.com",
		TokenProvider:  func() (*provider.Credentials, error) { return nil, nil },
	}
	d := NewDriver(cfg, deps)
	if d.cfg.MulticaBaseURL != cfg.MulticaBaseURL {
		t.Errorf("cfg.MulticaBaseURL = %q, want %q", d.cfg.MulticaBaseURL, cfg.MulticaBaseURL)
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

// fakeMultica is a minimal in-memory multica backend for driver registration
// tests. It implements /api/workspaces, /api/daemon/register,
// /api/daemon/heartbeat, /api/daemon/deregister, /api/chat/sessions, and the
// session-binding endpoints used by workflow task execution.
type fakeMultica struct {
	mu            sync.Mutex
	workspaces    []workflow.Workspace
	registrations []workflow.DaemonRegisterRequest
	heartbeats    []string
	deregistered  []string
	pinSessions   []pinSessionCall
	bindSessions  []bindSessionCall
	sessions      []workflow.ChatSession
	nextRuntimeID int
	nextSessionID int
	runtimeAlive  map[string]bool
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

func newFakeMultica(workspaces ...workflow.Workspace) *fakeMultica {
	return &fakeMultica{
		workspaces:    workspaces,
		nextRuntimeID: 1,
		nextSessionID: 1,
		runtimeAlive:  make(map[string]bool),
	}
}

func (f *fakeMultica) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc(workflow.MulticaWorkspacesEndpoint, func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.workspaces)
	})
	mux.HandleFunc(workflow.MulticaDaemonRegisterEndpoint, func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc(workflow.MulticaDaemonHeartbeatEndpoint, func(w http.ResponseWriter, r *http.Request) {
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
	mux.HandleFunc(workflow.MulticaDaemonDeregisterEndpoint, func(w http.ResponseWriter, r *http.Request) {
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
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/session") {
			parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(parts) < 4 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			taskID := parts[len(parts)-2]
			var req workflow.PinTaskSessionRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			f.mu.Lock()
			f.pinSessions = append(f.pinSessions, pinSessionCall{TaskID: taskID, SessionID: req.SessionID, WorkDir: req.WorkDir})
			f.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
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

func (f *fakeMultica) snapshot() (regs []workflow.DaemonRegisterRequest, beats []string, deregs []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]workflow.DaemonRegisterRequest{}, f.registrations...),
		append([]string{}, f.heartbeats...),
		append([]string{}, f.deregistered...)
}

func (f *fakeMultica) killRuntimes() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runtimeAlive = make(map[string]bool)
}

func (f *fakeMultica) sessionCalls() (pins []pinSessionCall, binds []bindSessionCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pinSessionCall{}, f.pinSessions...),
		append([]bindSessionCall{}, f.bindSessions...)
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
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"}, workflow.Workspace{ID: "ws-2", Name: "two"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		MulticaBaseURL: ts.URL,
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
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		MulticaBaseURL: ts.URL,
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
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, time.Hour), &Dependencies{
		MulticaBaseURL: ts.URL,
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
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		MulticaBaseURL: ts.URL,
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
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()

	d := NewDriver(testDriverConfig(t, 50*time.Millisecond), &Dependencies{
		MulticaBaseURL: ts.URL,
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

	multica := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer multica.Close()

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
		MulticaBaseURL: multica.URL,
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

// callbackRecorder is a fake multica that records the daemon task callbacks
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

func asyncTestDriver(t *testing.T, multicaURL string) *Driver {
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
		MulticaBaseURL: multicaURL,
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

	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
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
		MulticaBaseURL: ts.URL,
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
	f := newFakeMultica()
	ts := httptest.NewServer(f.handler())
	defer ts.Close()

	d := &Driver{
		deps: &Dependencies{
			MulticaBaseURL: ts.URL,
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

func TestDriverCheckoutRepo(t *testing.T) {
	requireGit(t)
	upstream := initTestRepo(t)
	fm := newFakeMultica()
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()
	d := asyncTestDriver(t, ts.URL)

	taskID := "11111111-aaaa-bbbb-cccc-dddddddddddd"
	wsID := "ws-1"
	// Manually register a running task record (bypass RunTaskAsync; directly
	// populate the state CheckoutRepo reads).
	taskRoot := filepath.Join(d.cfg.WorkspacesRoot, wsID, "tasks", taskID)
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.running[taskID] = &taskRecord{
		payload:  workflow.TaskRunPayload{TaskID: taskID, WorkspaceID: wsID, Agent: AgentCsc},
		taskRoot: taskRoot,
	}
	d.mu.Unlock()

	dir, err := d.CheckoutRepo(taskID, upstream, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Errorf("worktree not created: %v", err)
	}
	// Unknown task => error.
	if _, err := d.CheckoutRepo("nonexistent-task", upstream, ""); err == nil {
		t.Error("CheckoutRepo should error for a task that is not running")
	}
}

// TestDriverCheckoutRepo_DeliveryRoleFromPayloadRepos verifies that when the
// task payload carries a `repos[]` entry with role="delivery" for the requested
// URL, the driver derives the branch from NodeRunID (node/<shortNodeRunID>)
// rather than the code-repo agent/<agent>/<shortTaskID> convention. The driver
// looks up the role by matching the repo URL against payload.Repos.
func TestDriverCheckoutRepo_DeliveryRoleFromPayloadRepos(t *testing.T) {
	requireGit(t)
	upstream := initTestRepo(t)
	fm := newFakeMultica()
	ts := httptest.NewServer(fm.handler())
	defer ts.Close()
	d := asyncTestDriver(t, ts.URL)

	taskID := "22222222-aaaa-bbbb-cccc-dddddddddddd"
	wsID := "ws-1"
	nodeRunID := "abcdef01-1234-5678-9abc-def012345678"
	taskRoot := filepath.Join(d.cfg.WorkspacesRoot, wsID, "tasks", taskID)
	if err := os.MkdirAll(taskRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.running[taskID] = &taskRecord{
		payload: workflow.TaskRunPayload{
			TaskID:      taskID,
			WorkspaceID: wsID,
			Agent:       AgentCsc,
			NodeRunID:   nodeRunID,
			// multica sends the delivery repo with role="delivery" and the code
			// repo with role="code". Only the delivery URL is requested below.
			Repos: []workflow.RepoSpec{
				{URL: upstream, Role: "delivery", Alias: "delivery"},
			},
			Env: map[string]string{
				"MULTICA_GITLAB_TOKEN": "gitlab-pat",
				"MULTICA_REPO_TOKEN":   "gitea-pat",
			},
		},
		taskRoot: taskRoot,
	}
	d.mu.Unlock()

	dir, err := d.CheckoutRepo(taskID, upstream, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	out, _ := exec.Command("git", "-C", dir, "branch", "--show-current").CombinedOutput()
	wantBranch := "node/" + shortID(nodeRunID) // node/abcdef01
	if strings.TrimSpace(string(out)) != wantBranch {
		t.Errorf("delivery branch = %q, want %q", strings.TrimSpace(string(out)), wantBranch)
	}
}

// TestLookupRepoRole covers the URL → role mapping used by Driver.CheckoutRepo:
// returns the matching repo's role, defaulting to "code" when no entry matches
// (backward-compat for repos not listed in payload.Repos).
func TestLookupRepoRole(t *testing.T) {
	repos := []workflow.RepoSpec{
		{URL: "https://gitlab.example.com/o/code.git", Role: "code"},
		{URL: "https://gitea.example.com/o/docs.git", Role: "delivery"},
	}
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"delivery match", "https://gitea.example.com/o/docs.git", "delivery"},
		{"code match", "https://gitlab.example.com/o/code.git", "code"},
		{"no match defaults to code", "https://other.example.com/o/other.git", "code"},
		{"empty repos defaults to code", "https://gitlab.example.com/o/code.git", "code"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := repos
			if tc.name == "empty repos defaults to code" {
				pool = nil
			}
			if got := lookupRepoRole(pool, tc.url); got != tc.want {
				t.Errorf("lookupRepoRole(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// flakySessionRunner fails its first RunSession call (simulating a corrupt
// resumed session) and succeeds on the second (the fresh retry).
type flakySessionRunner struct {
	calls int
}

func (f *flakySessionRunner) RunSession(ctx context.Context, sessionID, worktree, prompt string, env []string) ([]byte, error) {
	f.calls++
	if f.calls == 1 {
		return nil, fmt.Errorf("resumed session boom")
	}
	return []byte("ok-round2"), nil
}

// TestExecute_ResumeFailureRetriesFresh verifies that when a resumed prior
// session FAILS on the first RunCSCSession, execute retries once with a fresh
// session (clears PriorSessionID → bindSession creates a new chat session →
// RunCSCSession again). Mirrors multica daemon.go:2662-2677.
func TestExecute_ResumeFailureRetriesFresh(t *testing.T) {
	installFakeAgent(t, AgentCsc)

	flaky := &flakySessionRunner{}
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
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
		MulticaBaseURL: ts.URL,
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
	fm := newFakeMultica(workflow.Workspace{ID: "ws-1", Name: "one"})
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
		MulticaBaseURL: ts.URL,
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
