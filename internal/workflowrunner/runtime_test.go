package workflowrunner

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/workflow"
)

func TestRuntimeSyncInterval(t *testing.T) {
	cfg := workflow.Config{SyncInterval: 10 * time.Millisecond}
	r := newRuntime(cfg, nil, nil)
	r.syncFunc = func() error { return nil }
	if err := r.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := r.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestRuntimeCallsSyncFunc(t *testing.T) {
	cfg := workflow.Config{SyncInterval: 10 * time.Millisecond}
	r := newRuntime(cfg, nil, nil)
	var calls atomic.Int32
	r.syncFunc = func() error {
		calls.Add(1)
		return nil
	}
	if err := r.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if err := r.Stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if calls.Load() == 0 {
		t.Fatal("syncFunc was not called")
	}
}

func TestRuntimeDoSyncWritesToCache(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":"ws-1","name":"Test"}]`))
	}))
	defer ts.Close()

	cfg := workflow.Config{SyncInterval: time.Hour, AgentTimeout: time.Minute}
	client := NewClient(ts.URL, "", func() (*provider.Credentials, error) {
		return &provider.Credentials{AccessToken: "x"}, nil
	})
	cache := workflow.NewCache(t.TempDir())
	r := newRuntime(cfg, client, cache)

	if err := r.doSync(); err != nil {
		t.Fatalf("doSync: %v", err)
	}
	if !called {
		t.Fatal("client was not called")
	}
	wss, err := cache.ReadWorkspaces()
	if err != nil {
		t.Fatalf("read cache: %v", err)
	}
	if len(wss) != 1 || wss[0].ID != "ws-1" {
		t.Fatalf("unexpected workspaces: %+v", wss)
	}
}
