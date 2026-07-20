package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/workflow"
)

func TestDriverImplementsPersistentDriver(t *testing.T) {
	var _ runtime.PersistentDriver = (*Driver)(nil)
}

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
// /api/daemon/heartbeat and /api/daemon/deregister.
type fakeMultica struct {
	mu            sync.Mutex
	workspaces    []workflow.Workspace
	registrations []workflow.DaemonRegisterRequest
	heartbeats    []string
	deregistered  []string
	nextRuntimeID int
	runtimeAlive  map[string]bool
}

func newFakeMultica(workspaces ...workflow.Workspace) *fakeMultica {
	return &fakeMultica{
		workspaces:    workspaces,
		nextRuntimeID: 1,
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
		_ = json.NewEncoder(w).Encode([]workflow.DaemonRuntimeResponse{
			{ID: rtID, WorkspaceID: req.WorkspaceID, Provider: "cs-cloud", Status: "online"},
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
		for _, id := range body["runtime_ids"].([]any) {
			s, _ := id.(string)
			f.deregistered = append(f.deregistered, s)
			delete(f.runtimeAlive, s)
		}
		w.WriteHeader(http.StatusOK)
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
		AllowedAgents:     []string{"sh"},
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
		if len(req.Runtimes) != 1 || req.Runtimes[0].Type != "cs-cloud" {
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

func TestDriverAbortTask(t *testing.T) {
	multica := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer multica.Close()

	cfg := workflow.Config{
		WorkspacesRoot: t.TempDir(),
		CacheDir:       t.TempDir(),
		SyncInterval:   time.Hour,
		GCInterval:     time.Hour,
		AgentTimeout:   time.Minute,
		AllowedAgents:  []string{"sh"},
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
			Agent:       "sh",
			Prompt:      "sleep 10",
		})
	}()

	time.Sleep(100 * time.Millisecond)
	if err := d.AbortTask("task-1"); err != nil {
		t.Fatalf("abort: %v", err)
	}

	wg.Wait()
	if runErr == nil {
		t.Fatal("expected RunTask to return an error after abort")
	}
}

