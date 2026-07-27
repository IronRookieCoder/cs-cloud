package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/version"
	"cs-cloud/internal/workflow"
)

// providerCSCloud is the multica runtime provider value the issue-conversation
// flow searches for; registration must use exactly this string.
const providerCSCloud = "cs-cloud"

// deregisterTimeout bounds the best-effort deregister call on Stop.
const deregisterTimeout = 10 * time.Second

// abortTombstoneTTL is how long an abort tombstone for a not-yet-started
// task is remembered (the abort can race ahead of the pushed run request).
const abortTombstoneTTL = time.Hour

// maxCallbackOutputBytes caps the output uploaded to multica per task.
const maxCallbackOutputBytes = 256 * 1024

// taskRecord tracks a running task so it can be aborted.
type taskRecord struct {
	cancel  context.CancelFunc
	aborted bool
	// payload + taskRoot let the localserver's repo-checkout RPC serve the
	// running task's context (agent name, workspace, env token, cwd) without
	// the CLI having to re-send them.
	payload  workflow.TaskRunPayload
	taskRoot string
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
	// abortedIDs tombstones task IDs aborted before their run request
	// arrived; reserve rejects them so a cancelled task never executes.
	abortedIDs map[string]time.Time
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
	if d.deps.MulticaBaseURL == "" {
		d.state = driverStateError
		return fmt.Errorf("workflow multica base URL is required")
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

	logger.Info("workflow: multica base URL=%s user base URL=%s", d.deps.MulticaBaseURL, d.deps.UserBaseURL)
	cache := workflow.NewCache(d.cfg.CacheDir)
	d.client = NewClient(d.deps.MulticaBaseURL, d.deps.UserBaseURL, d.deps.TokenProvider)
	d.runtime = newRuntime(d.cfg, d.client, cache)
	d.runtime.maintainFunc = d.maintainRegistrations
	d.runner = NewTaskRunner(d.workspaceManager, d.cfg.AgentTimeout, d.cfg.AllowedAgents)
	if d.deps != nil && d.deps.SessionRunner != nil {
		d.runner.SetSessionRunner(d.deps.SessionRunner)
	}
	// Inject multica endpoint + token so in-task CLIs (cs-cloud gitea
	// submit/fetch) get MULTICA_SERVER_URL + MULTICA_TOKEN in their env.
	if d.deps != nil {
		d.runner.SetMulticaEndpoint(d.deps.MulticaBaseURL, d.deps.TokenProvider)
	}
	d.sem = make(chan struct{}, d.cfg.MaxConcurrentTasks)
	d.running = make(map[string]*taskRecord)
	d.abortedIDs = make(map[string]time.Time)
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
	d.abortedIDs = nil
	d.state = driverStateError
}

// Stop halts the runtime loop and clears the running state.
func (d *Driver) Stop() error {
	d.mu.Lock()
	rt := d.runtime
	d.mu.Unlock()
	// Stop the runtime loop WITHOUT holding d.mu: the maintain goroutine
	// acquires d.mu, so waiting for it (wg.Wait) under the lock deadlocks.
	if rt != nil {
		_ = rt.Stop()
	}

	d.mu.Lock()
	defer d.mu.Unlock()

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

// RunTask executes a task payload synchronously. It returns an error if the
// driver is not running or if task execution fails.
func (d *Driver) RunTask(ctx context.Context, payload workflow.TaskRunPayload) error {
	rec, err := d.reserve(payload)
	if err != nil {
		return err
	}
	defer d.release(payload.TaskID, rec)

	ctx, cancel := context.WithTimeout(ctx, d.cfg.AgentTimeout)
	defer cancel()
	d.armCancel(rec, cancel)

	return d.execute(ctx, payload, rec)
}

// RunTaskAsync reserves the task synchronously — rejecting duplicates, a
// full queue, and pre-aborted tasks before the caller responds — and then
// executes it in a detached goroutine with its own timeout context. The
// localserver handler uses this so the HTTP response is not held for the
// whole agent run (the gateway proxy caps requests at ~30s).
func (d *Driver) RunTaskAsync(payload workflow.TaskRunPayload) error {
	rec, err := d.reserve(payload)
	if err != nil {
		return err
	}
	go func() {
		defer d.release(payload.TaskID, rec)
		ctx, cancel := context.WithTimeout(context.Background(), d.cfg.AgentTimeout)
		defer cancel()
		d.armCancel(rec, cancel)
		if err := d.execute(ctx, payload, rec); err != nil {
			logger.Warn("workflow: task %s failed: %v", payload.TaskID, err)
		}
	}()
	return nil
}

// reserve claims a semaphore slot (failing fast when full) and registers the
// task as running. It also rejects tasks whose abort arrived before the run
// request (cancel raced the server-side push).
func (d *Driver) reserve(payload workflow.TaskRunPayload) (*taskRecord, error) {
	if err := d.Health(); err != nil {
		return nil, err
	}

	select {
	case d.sem <- struct{}{}:
	default:
		return nil, fmt.Errorf("too many running tasks")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.running[payload.TaskID]; exists {
		<-d.sem
		return nil, fmt.Errorf("task %s is already running", payload.TaskID)
	}
	if ts, wasAborted := d.abortedIDs[payload.TaskID]; wasAborted {
		<-d.sem
		if time.Since(ts) <= abortTombstoneTTL {
			delete(d.abortedIDs, payload.TaskID)
			return nil, fmt.Errorf("task %s was aborted before it started", payload.TaskID)
		}
		delete(d.abortedIDs, payload.TaskID)
	}
	// GC expired tombstones while we hold the lock.
	for id, ts := range d.abortedIDs {
		if time.Since(ts) > abortTombstoneTTL {
			delete(d.abortedIDs, id)
		}
	}

	rec := &taskRecord{}
	d.running[payload.TaskID] = rec
	return rec, nil
}

// release unregisters the task and frees its semaphore slot.
func (d *Driver) release(taskID string, rec *taskRecord) {
	d.mu.Lock()
	delete(d.running, taskID)
	d.mu.Unlock()
	<-d.sem
}

// armCancel wires the execution context's cancel into the task record so
// AbortTask can stop the run; if the abort landed first, cancel immediately.
func (d *Driver) armCancel(rec *taskRecord, cancel context.CancelFunc) {
	d.mu.Lock()
	rec.cancel = cancel
	aborted := rec.aborted
	d.mu.Unlock()
	if aborted {
		cancel()
	}
}

// execute runs the agent and reports the outcome to multica.
func (d *Driver) execute(ctx context.Context, payload workflow.TaskRunPayload, rec *taskRecord) error {
	if err := d.client.StartTask(ctx, payload.TaskID); err != nil {
		// multica rejected the start (e.g. the task was cancelled between
		// dispatch and device accept) — abort locally without reporting a
		// failure for a task that is already finalized server-side.
		if rec.cancel != nil {
			rec.cancel()
		}
		return fmt.Errorf("start task: %w", err)
	}

	// Bind a chat session to the task and its workflow node run so the web
	// UI can enter the live session. This must happen before the agent
	// produces output, otherwise "进入会话" would point at nothing.
	worktree, agentPath, err := d.runner.Prepare(ctx, payload)
	if err != nil {
		_ = d.client.FailTask(ctx, payload.TaskID, err.Error(), "")
		return err
	}
	// Record payload + taskRoot on the running task so the localserver's
	// repo-checkout RPC can serve the task's context without the CLI
	// re-sending it. worktree is the task root after Task 6's Prepare change.
	d.mu.Lock()
	if rec, ok := d.running[payload.TaskID]; ok {
		rec.payload = payload
		rec.taskRoot = worktree
	}
	d.mu.Unlock()

	sessionID, err := d.bindSession(ctx, payload, worktree)
	if err != nil {
		_ = d.client.FailTask(ctx, payload.TaskID, err.Error(), "")
		return err
	}

	var out []byte
	var runErr error
	if payload.Agent == AgentCsc && sessionID != "" && d.deps != nil && d.deps.SessionRunner != nil {
		out, runErr = d.runner.RunCSCSession(ctx, payload, worktree, sessionID)
		// Resume failure fallback: if resuming the prior session failed to make
		// progress, retry once with a fresh session (the prior session may be
		// corrupt on disk). Mirrors multica daemon.go:2662-2677.
		if runErr != nil && payload.PriorSessionID != "" && !d.aborted(payload.TaskID) {
			logger.Warn("workflow: resumed session failed (%v); retrying with fresh session", runErr)
			payload.PriorSessionID = "" // force bindSession to create a fresh chat session
			freshSessionID, bindErr := d.bindSession(ctx, payload, worktree)
			if bindErr != nil {
				_ = d.client.FailTask(ctx, payload.TaskID, bindErr.Error(), "")
				return bindErr
			}
			out, runErr = d.runner.RunCSCSession(ctx, payload, worktree, freshSessionID)
		}
	} else {
		out, runErr = d.runner.RunPrepared(ctx, payload, worktree, agentPath)
	}
	output := truncateOutput(string(out))
	_ = d.client.PostTaskMessages(ctx, payload.TaskID, output)
	if runErr != nil {
		if d.aborted(payload.TaskID) {
			_ = d.client.FailTask(ctx, payload.TaskID, "aborted", "cancelled")
		} else {
			_ = d.client.FailTask(ctx, payload.TaskID, runErr.Error(), "")
		}
		return runErr
	}

	return d.client.CompleteTask(ctx, payload.TaskID, output)
}

// bindSession creates a chat session for the task and binds it to both the
// task row and the workflow node run. It returns the chat session row UUID.
// When the payload does not carry the agent/node-run IDs the driver needs, or
// when the driver has not registered a runtime for the workspace, it returns
// ("", nil) and execution continues without a session.
func (d *Driver) bindSession(ctx context.Context, payload workflow.TaskRunPayload, worktree string) (string, error) {
	if payload.AgentID == "" || payload.NodeRunID == "" {
		return "", nil
	}
	if d.deps == nil || d.deps.DeviceID == nil {
		return "", nil
	}

	d.mu.Lock()
	runtimeID, registered := d.registrations[payload.WorkspaceID]
	d.mu.Unlock()
	if !registered || runtimeID == "" {
		return "", nil
	}

	deviceID, err := d.deps.DeviceID()
	if err != nil {
		return "", fmt.Errorf("resolve device id: %w", err)
	}
	if deviceID == "" {
		return "", nil
	}

	// Resume: reuse the prior csc session id (still on disk in csc serve's
	// store). Skip CreateChatSession so the conversation carries forward across
	// rounds of the same (agent, issue). First round: create a new chat session.
	sessionID := payload.PriorSessionID
	if sessionID == "" {
		session, err := d.client.CreateChatSession(ctx, payload.WorkspaceID, payload.AgentID, chatSessionTitle(payload))
		if err != nil {
			return "", fmt.Errorf("create chat session: %w", err)
		}
		if session.ID == "" {
			return "", fmt.Errorf("multica returned empty chat session id")
		}
		sessionID = session.ID
	}

	// Pin the real workdir (task root) so the next round's prior_work_dir hits.
	if err := d.client.PinTaskSession(ctx, payload.TaskID, sessionID, worktree); err != nil {
		return "", fmt.Errorf("pin task session: %w", err)
	}
	if err := d.client.BindNodeRunSession(ctx, payload.NodeRunID, runtimeID, deviceID, sessionID); err != nil {
		return "", fmt.Errorf("bind node run session: %w", err)
	}

	if d.deps.ConversationBinder != nil {
		// Create the csc session with the task env so in-task CLIs (notably
		// `cs-cloud workflow deliverable submit`, which needs MULTICA_TOKEN +
		// MULTICA_GITEA_* to push document deliverables to Gitea) inherit the
		// credentials multica pushed in the task payload. RunSession reuses
		// this session, so the env must be present at creation.
		env := d.runner.buildEnv(payload, worktree)
		if err := d.deps.ConversationBinder.Bind(ctx, sessionID, worktree, env); err != nil {
			logger.Warn("workflow: failed to bind local conversation session %s: %v", sessionID, err)
		}
	}

	logger.Info("workflow: bound session %s to task %s node_run %s", sessionID, payload.TaskID, payload.NodeRunID)
	return sessionID, nil
}

func chatSessionTitle(payload workflow.TaskRunPayload) string {
	if payload.IssueID != "" {
		return fmt.Sprintf("Workflow session for issue %s", payload.IssueID)
	}
	return fmt.Sprintf("Workflow session for task %s", payload.TaskID)
}

// truncateOutput caps callback payloads at maxCallbackOutputBytes, staying
// on a valid UTF-8 boundary.
func truncateOutput(s string) string {
	if len(s) <= maxCallbackOutputBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxCallbackOutputBytes], "") + "\n... (output truncated)"
}

// CheckoutRepo serves an agent's on-demand `cs-cloud repo checkout` for a
// running task: looks up the task's payload + taskRoot, then creates (or resets)
// a per-repo branch worktree. baseBranch "" resolves the remote default.
func (d *Driver) CheckoutRepo(taskID, repoURL, baseBranch string) (string, error) {
	d.mu.Lock()
	rec, ok := d.running[taskID]
	d.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("task %s is not running", taskID)
	}
	token := rec.payload.Env["MULTICA_GITLAB_TOKEN"]
	return d.workspaceManager.CheckoutRepo(
		rec.payload.WorkspaceID, rec.taskRoot, repoURL,
		rec.payload.Agent, taskID, baseBranch, token,
	)
}

// SetLocalServerURL threads the daemon's localserver listen URL to the task
// runner so it can be injected into the agent env (CS_CLOUD_SERVER_URL),
// letting in-task `cs-cloud repo checkout` reach the localserver RPC. The
// nil-guard on d.runner is defensive — runner is set in Start(), and the
// daemon calls this after Start() has succeeded.
func (d *Driver) SetLocalServerURL(url string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.runner != nil {
		d.runner.SetLocalServerURL(url)
	}
}

// AbortTask cancels a running task. When the task is not (yet) running, the
// ID is tombstoned so a run request that arrives later — the abort raced
// ahead of the server-side push — is rejected instead of executed.
func (d *Driver) AbortTask(taskID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.running[taskID]
	if !ok {
		if d.abortedIDs != nil {
			d.abortedIDs[taskID] = time.Now()
		}
		return fmt.Errorf("task %s is not running", taskID)
	}
	rec.aborted = true
	if rec.cancel != nil {
		rec.cancel()
	}
	return nil
}

func (d *Driver) aborted(taskID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	rec, ok := d.running[taskID]
	return ok && rec.aborted
}

// SetConversationBinder injects the conversation binder after construction.
// It must be called before Start.
func (d *Driver) SetConversationBinder(binder ConversationBinder) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deps == nil {
		d.deps = &Dependencies{}
	}
	d.deps.ConversationBinder = binder
	if r, ok := binder.(SessionRunner); ok {
		d.deps.SessionRunner = r
	}
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
	if deviceID == "" {
		return nil
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
