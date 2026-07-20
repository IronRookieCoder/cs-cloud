package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/version"
	"cs-cloud/internal/workflow"
)

// Compile-time check that Driver implements runtime.PersistentDriver.
var _ runtime.PersistentDriver = (*Driver)(nil)

// providerCSCloud is the multica runtime provider value the issue-conversation
// flow searches for; registration must use exactly this string.
const providerCSCloud = "cs-cloud"

// deregisterTimeout bounds the best-effort deregister call on Stop.
const deregisterTimeout = 10 * time.Second

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
	// registrations maps workspace ID → multica runtime row ID, kept alive
	// by the maintain loop.
	registrations map[string]string
	mu            sync.Mutex
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
	d.runtime.maintainFunc = d.maintainRegistrations
	d.runner = NewTaskRunner(d.workspaceManager, d.cfg.AgentTimeout, d.cfg.AllowedAgents)
	d.sem = make(chan struct{}, d.cfg.MaxConcurrentTasks)
	d.running = make(map[string]*taskRecord)
	d.registrations = make(map[string]string)

	if err := d.runtime.Start(); err != nil {
		d.cleanupOnError()
		return err
	}

	// Register with multica right away instead of waiting for the first
	// heartbeat tick. Async so a slow/unreachable multica doesn't block
	// daemon startup; failures are retried by the maintain loop.
	go func() {
		if err := d.maintainRegistrations(); err != nil {
			logger.Warn("workflow: initial multica registration failed: %v", err)
		}
	}()

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

	// Tell multica these runtimes went away so the runtime page doesn't
	// wait for the sweeper to mark them offline. Best-effort: a
	// dead multica must not delay daemon shutdown.
	ids := make([]string, 0, len(d.registrations))
	for _, id := range d.registrations {
		ids = append(ids, id)
	}
	d.registrations = make(map[string]string)
	if len(ids) > 0 && d.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), deregisterTimeout)
		defer cancel()
		if err := d.client.DeregisterDaemon(ctx, ids); err != nil {
			logger.Warn("workflow: multica deregister failed: %v", err)
		}
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

// maintainRegistrations keeps the multica runtime rows for every workspace
// alive: register the missing ones, heartbeat the rest, and re-register any
// row multica dropped (heartbeat 404). Called once at startup and then on
// every heartbeat tick by the runtime loop.
func (d *Driver) maintainRegistrations() error {
	if d.deps == nil || d.deps.DeviceID == nil || d.client == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.AgentTimeout)
	defer cancel()

	deviceID, err := d.deps.DeviceID()
	if err != nil {
		return fmt.Errorf("resolve device id: %w", err)
	}

	workspaces, err := d.client.GetWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	listed := make(map[string]bool, len(workspaces))
	for _, ws := range workspaces {
		listed[ws.ID] = true
	}

	d.mu.Lock()
	for workspaceID, runtimeID := range d.registrations {
		if listed[workspaceID] {
			continue
		}
		// Workspace disappeared from the listing (token scope changed?);
		// stop heartbeating it but leave the row for the sweeper.
		delete(d.registrations, workspaceID)
		logger.Warn("workflow: workspace %s no longer listed, dropping registration %s", workspaceID, runtimeID)
	}
	d.mu.Unlock()

	var firstErr error
	for _, ws := range workspaces {
		if err := d.maintainWorkspace(ctx, ws.ID, deviceID); err != nil {
			logger.Warn("workflow: maintain registration for workspace %s: %v", ws.ID, err)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// maintainWorkspace heartbeats an existing registration or registers the
// workspace if it has none. A 404 from heartbeat means multica deleted the
// runtime row (sweeper or restart), so re-register immediately.
func (d *Driver) maintainWorkspace(ctx context.Context, workspaceID, deviceID string) error {
	d.mu.Lock()
	runtimeID, registered := d.registrations[workspaceID]
	d.mu.Unlock()

	if registered {
		err := d.client.Heartbeat(ctx, runtimeID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrRuntimeGone) {
			return err
		}
		d.mu.Lock()
		delete(d.registrations, workspaceID)
		d.mu.Unlock()
	}

	return d.registerWorkspace(ctx, workspaceID, deviceID)
}

// registerWorkspace registers this daemon's cs-cloud runtime for one
// workspace and records the returned runtime row ID.
func (d *Driver) registerWorkspace(ctx context.Context, workspaceID, deviceID string) error {
	hostname, _ := os.Hostname()
	req := workflow.DaemonRegisterRequest{
		WorkspaceID: workspaceID,
		DaemonID:    deviceID,
		DeviceName:  hostname,
		CLIVersion:  version.Get(),
		Runtimes: []workflow.DaemonRuntime{
			{
				Name:    providerCSCloud,
				Type:    providerCSCloud,
				Version: version.Get(),
				Status:  "online",
			},
		},
	}

	rows, err := d.client.RegisterDaemon(ctx, req)
	if err != nil {
		return err
	}
	if len(rows) == 0 || rows[0].ID == "" {
		return fmt.Errorf("register daemon returned no runtime id")
	}

	d.mu.Lock()
	d.registrations[workspaceID] = rows[0].ID
	d.mu.Unlock()
	logger.Info("workflow: registered cs-cloud runtime %s for workspace %s", rows[0].ID, workspaceID)
	return nil
}
