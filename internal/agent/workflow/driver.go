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

// Driver is a persistent driver for the cs-workflow subsystem.
type Driver struct {
	cfg              workflow.Config
	deps             *Dependencies
	workspaceManager *WorkspaceManager
	client           *Client
	runtime          *runtimeLoop
	runner           *TaskRunner
	state            driverState
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

	d.workspaceManager = NewWorkspaceManager(d.cfg.WorkspacesRoot)
	if err := d.workspaceManager.EnsureRoot(); err != nil {
		d.state = driverStateError
		return err
	}

	cache := workflow.NewCache(d.cfg.CacheDir)
	d.client = NewClient(d.deps.MulticaBaseURL, d.deps.TokenProvider)
	d.runtime = newRuntime(d.cfg, d.client, cache)
	d.runner = NewTaskRunner(d.workspaceManager, d.client, d.cfg.AgentTimeout)

	if err := d.runtime.Start(); err != nil {
		d.state = driverStateError
		return err
	}

	d.state = driverStateRunning
	return nil
}

// Stop halts the runtime loop and clears the running state.
func (d *Driver) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runtime != nil {
		_ = d.runtime.Stop()
	}
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
func (d *Driver) RunTask(payload workflow.TaskRunPayload) error {
	if err := d.Health(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.AgentTimeout)
	defer cancel()
	return d.runner.Run(ctx, payload)
}

// tokenProvider returns the configured credential provider, or nil if deps
// have not been supplied.
func (d *Driver) tokenProvider() func() (*provider.Credentials, error) {
	if d.deps == nil {
		return nil
	}
	return d.deps.TokenProvider
}
