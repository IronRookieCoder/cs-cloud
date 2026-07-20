package workflow

import (
	"context"
	"sync"
	"time"

	"cs-cloud/internal/workflow"
)

// runtimeLoop runs background sync and GC loops for the workflow driver.
type runtimeLoop struct {
	cfg      workflow.Config
	client   *Client
	cache    *workflow.Cache
	syncFunc func() error
	gcFunc   func() error
	// maintainFunc keeps multica daemon registrations alive (register +
	// heartbeat + re-register). Nil disables the maintain loop.
	maintainFunc func() error

	mu      sync.Mutex
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// newRuntime creates a new runtimeLoop.
func newRuntime(cfg workflow.Config, client *Client, cache *workflow.Cache) *runtimeLoop {
	return &runtimeLoop{
		cfg:    cfg,
		client: client,
		cache:  cache,
	}
}

// Start begins the background sync and GC loops.
func (r *runtimeLoop) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.started {
		return nil
	}

	r.ctx, r.cancel = context.WithCancel(context.Background())
	r.wg.Add(3)
	r.started = true
	go r.loop(r.cfg.SyncInterval, r.doSync)
	go r.loop(r.cfg.GCInterval, r.doGC)
	go r.loop(r.cfg.HeartbeatInterval, r.doMaintain)
	return nil
}

// Stop cancels the background loops and waits for them to finish.
func (r *runtimeLoop) Stop() error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	r.started = false
	r.mu.Unlock()
	r.wg.Wait()
	return nil
}

func (r *runtimeLoop) loop(interval time.Duration, fn func() error) {
	defer r.wg.Done()
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			_ = fn()
		}
	}
}

func (r *runtimeLoop) doSync() error {
	if r.syncFunc != nil {
		return r.syncFunc()
	}
	if r.client == nil || r.cache == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), r.cfg.AgentTimeout)
	defer cancel()
	wss, err := r.client.GetWorkspaces(ctx)
	if err != nil {
		return err
	}
	return r.cache.WriteWorkspaces(wss)
}

func (r *runtimeLoop) doGC() error {
	if r.gcFunc != nil {
		return r.gcFunc()
	}
	return nil
}

func (r *runtimeLoop) doMaintain() error {
	if r.maintainFunc != nil {
		return r.maintainFunc()
	}
	return nil
}
