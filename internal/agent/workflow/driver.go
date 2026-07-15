package workflow

import (
	"context"
	"fmt"
	"sync"

	"cs-cloud/internal/provider"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/workflow"
)

// Compile-time check that Driver implements runtime.PersistentDriver.
var _ runtime.PersistentDriver = (*Driver)(nil)

// taskRecord tracks a running task so it can be aborted.
type taskRecord struct {
	cancel  context.CancelFunc
	aborted bool
}

// Driver is a persistent driver for the cs-workflow subsystem.
type Driver struct {
	cfg              workflow.Config
	deps             *Dependencies
	workspaceManager *WorkspaceManager
	client           *Client
	runtime          *runtimeLoop
	runner           *TaskRunner
	state            driverState
	sem              chan struct{}
	running          map[string]*taskRecord
	mu               sync.Mutex
}

// NewDriver creates a new workflow driver.
func NewDriver(cfg workflow.Config, deps *Dependencies) *Driver {
	return &Driver{cfg: cfg, deps: deps}
}

// Name returns the driver name.
func (d *Driver) Name() string { return "workflow" }

// Start initializes the workflow driver components and starts the runtime loop.
func (d *Driver) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.state == driverStateRunning {
		return nil
	}
	if d.deps == nil {
		d.state = driverStateError
		return fmt.Errorf("workflow driver dependencies not provided")
	}

	if d.cfg.MaxConcurrentTasks <= 0 {
		d.cfg.MaxConcurrentTasks = workflow.DefaultConfig().MaxConcurrentTasks
	}
	if d.cfg.AgentTimeout <= 0 {
		d.cfg.AgentTimeout = workflow.DefaultConfig().AgentTimeout
	}

	d.workspaceManager = NewWorkspaceManager(d.cfg.WorkspacesRoot)
	if err := d.workspaceManager.EnsureRoot(); err != nil {
		d.cleanupOnError()
		return err
	}

	cache := workflow.NewCache(d.cfg.CacheDir)
	d.client = NewClient(d.deps.MulticaBaseURL, d.deps.TokenProvider)
	d.runtime = newRuntime(d.cfg, d.client, cache)
	d.runner = NewTaskRunner(d.workspaceManager, d.cfg.AgentTimeout, d.cfg.AllowedAgents)
	d.sem = make(chan struct{}, d.cfg.MaxConcurrentTasks)
	d.running = make(map[string]*taskRecord)

	if err := d.runtime.Start(); err != nil {
		d.cleanupOnError()
		return err
	}

	d.state = driverStateRunning
	return nil
}

func (d *Driver) cleanupOnError() {
	if d.runtime != nil {
		_ = d.runtime.Stop()
	}
	d.workspaceManager = nil
	d.client = nil
	d.runtime = nil
	d.runner = nil
	d.sem = nil
	d.running = nil
	d.state = driverStateError
}

// Stop halts the runtime loop and clears the running state.
func (d *Driver) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtime != nil {
		_ = d.runtime.Stop()
	}
	for _, rec := range d.running {
		if rec.cancel != nil {
			rec.cancel()
		}
	}
	d.running = make(map[string]*taskRecord)
	d.state = driverStateIdle
	return nil
}

// Health returns an error when the driver is not running.
func (d *Driver) Health() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.state != driverStateRunning {
		return fmt.Errorf("workflow driver not running")
	}
	return nil
}

// RunTask executes a task payload. It returns an error if the driver is not
// running or if task execution fails.
func (d *Driver) RunTask(ctx context.Context, payload workflow.TaskRunPayload) error {
	if err := d.Health(); err != nil {
		return err
	}

	select {
	case d.sem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-d.sem }()

	ctx, cancel := context.WithTimeout(ctx, d.cfg.AgentTimeout)

	d.mu.Lock()
	if _, exists := d.running[payload.TaskID]; exists {
		d.mu.Unlock()
		cancel()
		return fmt.Errorf("task %s is already running", payload.TaskID)
	}
	rec := &taskRecord{cancel: cancel}
	d.running[payload.TaskID] = rec
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.running, payload.TaskID)
		d.mu.Unlock()
		cancel()
	}()

	if err := d.client.StartTask(ctx, payload.TaskID); err != nil {
		return err
	}

	out, err := d.runner.Run(ctx, payload)
	if err != nil {
		_ = d.client.PostTaskMessages(ctx, payload.TaskID, string(out))
		if d.aborted(payload.TaskID) {
			_ = d.client.FailTask(ctx, payload.TaskID, "aborted")
		} else {
			_ = d.client.FailTask(ctx, payload.TaskID, err.Error())
		}
		return err
	}

	_ = d.client.PostTaskMessages(ctx, payload.TaskID, string(out))
	return d.client.CompleteTask(ctx, payload.TaskID, map[string]any{"status": "ok"})
}

// AbortTask cancels a running task and reports it as aborted to multica.
func (d *Driver) AbortTask(taskID string) error {
	d.mu.Lock()
	rec, ok := d.running[taskID]
	if !ok {
		d.mu.Unlock()
		return fmt.Errorf("task %s is not running", taskID)
	}
	rec.aborted = true
	if rec.cancel != nil {
		rec.cancel()
	}
	d.mu.Unlock()
	return nil
}

func (d *Driver) aborted(taskID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.running[taskID]
	return ok && rec.aborted
}

// tokenProvider returns the configured credential provider, or nil if deps
// have not been supplied.
func (d *Driver) tokenProvider() func() (*provider.Credentials, error) {
	if d.deps == nil {
		return nil
	}
	return d.deps.TokenProvider
}
