package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/provider"
	"cs-cloud/internal/version"
	"cs-cloud/internal/workflow"
)

// providerCSCloud is the runtime provider value multica's issue-conversation
// flow filters runtimes by (server/internal/handler/issue_conversation.go:
// csCloudRuntimeProvider). This is a cross-repo wire contract — it MUST stay
// in lockstep with multica's constant, and is NOT the local agent CLI name
// ("csc"). Registering "csc" here makes multica return 503 "cs-cloud device
// not online" for every issue conversation.
const providerCSCloud = "cs-cloud"

// deregisterTimeout bounds the best-effort deregister call on Stop.
const deregisterTimeout = 10 * time.Second

// taskCallbackTimeout bounds terminal status callbacks independently from the
// execution context. The execution context is normally already cancelled when
// reporting a timeout, so reusing it would prevent FailTask from being sent.
const taskCallbackTimeout = 30 * time.Second

// sessionAbortTimeout bounds best-effort cleanup of a timed-out CSC session.
const sessionAbortTimeout = 5 * time.Second

// abortTombstoneTTL is how long an abort tombstone for a not-yet-started
// task is remembered (the abort can race ahead of the pushed run request).
const abortTombstoneTTL = time.Hour

// maxCallbackOutputBytes caps the output uploaded to the server per task.
const maxCallbackOutputBytes = 256 * 1024

// taskRecord tracks a running task so it can be aborted.
type taskRecord struct {
	cancel  context.CancelFunc
	aborted bool
	// payload is written once under d.mu in execute after Prepare; CheckoutRepo
	// reads its fields without the lock. Safe only while payload is treated as
	// read-only after that write — all current readers (CheckoutRepo, buildEnv,
	// the pre-warm goroutine) treat it as immutable.
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
	// registrations maps workspace ID → runtime row ID, kept alive
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
	if d.deps.BackendBaseURL == "" {
		d.state = driverStateError
		return fmt.Errorf("workflow server base URL is required")
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

	logger.Info("workflow: server base URL=%s user base URL=%s", d.deps.BackendBaseURL, d.deps.UserBaseURL)
	cache := workflow.NewCache(d.cfg.CacheDir)
	d.client = NewClient(d.deps.BackendBaseURL, d.deps.UserBaseURL, d.deps.TokenProvider)
	d.runtime = newRuntime(d.cfg, d.client, cache)
	d.runtime.maintainFunc = d.maintainRegistrations
	if d.cfg.GCEnabled {
		// Plug the GC decision state machine (gc.go) into the runtime loop's
		// existing gcFunc slot. The loop already ticks on GCInterval; this just
		// fills in the work each tick does.
		d.runtime.gcFunc = d.runGC
		logger.Info("workflow: gc enabled: interval=%s ttl=%s orphan_ttl=%s artifact_ttl=%s",
			d.cfg.GCInterval, d.cfg.GCTTL, d.cfg.GCOrphanTTL, d.cfg.GCArtifactTTL)
	} else {
		logger.Info("workflow: gc disabled")
	}
	d.runner = NewTaskRunner(d.workspaceManager, d.cfg.AgentTimeout, d.cfg.AllowedAgents)
	if d.deps != nil && d.deps.AgentEnv != nil {
		d.runner.SetAgentEnv(d.deps.AgentEnv)
	}
	if d.deps != nil && d.deps.SessionRunner != nil {
		d.runner.SetSessionRunner(d.deps.SessionRunner)
	}
	// Inject server endpoint + token so in-task CLIs (cs-cloud gitea
	// submit/fetch) get CS_CLOUD_BACKEND_URL + CS_CLOUD_TOKEN in their env.
	if d.deps != nil {
		d.runner.SetServerEndpoint(d.deps.BackendBaseURL, d.deps.TokenProvider)
	}
	d.sem = make(chan struct{}, d.cfg.MaxConcurrentTasks)
	d.running = make(map[string]*taskRecord)
	d.abortedIDs = make(map[string]time.Time)
	d.registrations = make(map[string]string)

	if err := d.runtime.Start(); err != nil {
		d.cleanupOnError()
		return err
	}

	// Register with the server right away instead of waiting for the first
	// heartbeat tick. Async so a slow/unreachable server doesn't block
	// daemon startup; failures are retried by the maintain loop.
	go func() {
		if err := d.maintainRegistrations(); err != nil {
			logger.Warn("workflow: initial server registration failed: %v", err)
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

	// Tell the server these runtimes went away so the runtime page doesn't
	// wait for the sweeper to mark them offline. Best-effort: a
	// dead server must not delay daemon shutdown.
	ids := make([]string, 0, len(d.registrations))
	for _, id := range d.registrations {
		ids = append(ids, id)
	}
	d.registrations = make(map[string]string)
	if len(ids) > 0 && d.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), deregisterTimeout)
		defer cancel()
		if err := d.client.DeregisterDaemon(ctx, ids); err != nil {
			logger.Warn("workflow: server deregister failed: %v", err)
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
		// execute owns all task-status callbacks + failure logging (failTask,
		// StartTask warn); a duplicate "task failed" here just double-logs.
		_ = d.execute(ctx, payload, rec)
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

// execute runs the agent and reports the outcome to the server.
func (d *Driver) execute(ctx context.Context, payload workflow.TaskRunPayload, rec *taskRecord) error {
	if err := d.client.StartTask(ctx, payload.TaskID); err != nil {
		// the server rejected the start (e.g. the task was cancelled between
		// dispatch and device accept) — abort locally without reporting a
		// failure for a task that is already finalized server-side.
		logger.Warn("workflow: task %s not started (rejected/cancelled by server): %v", payload.TaskID, err)
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
		return d.failTask(payload.TaskID, err, "")
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

	// Write GC metadata so the gcLoop can reclaim this workdir once the task's
	// parent record (issue / node-run / task) reaches a terminal state. The
	// completion hook below rewrites it with the real finish time.
	writeGCMetaForTask(worktree, payload, time.Time{})

	logger.Info("workflow: task %s dispatched: agent=%s kind=%s node_run=%s workdir=%s repos=%s deliverables=%s env_keys=%s plugin=%s cloud_skills=%d resume=%v",
		payload.TaskID, payload.Agent, payload.Kind, payload.NodeRunID, worktree,
		repoSummary(payload.Repos, payload.RepoURL),
		deliverableSummary(payload.Deliverables),
		envKeySummary(payload.Env),
		pluginName(payload.Plugin), len(payload.CloudSkills), payload.PriorSessionID != "")

	// Install the agent's configured plugin and cloud skills into the task
	// workdir before the csc session runs. csc resolves plugins (-s local) and
	// skills (--scope project) by cwd, and the bound session's cwd is this
	// workdir, so installed addons become visible to the run. Fail-closed: a
	// configured addon that cannot install means the task cannot run
	// meaningfully (mirrors multica's execenv.Prepare).
	if err := installCSCAddons(ctx, agentPath, worktree, payload, d.runner.buildEnv(payload, worktree)); err != nil {
		return d.failTask(payload.TaskID, err, "addon_install_failed")
	}

	sessionID, err := d.bindSession(ctx, payload, worktree)
	if err != nil {
		return d.failTask(payload.TaskID, err, "")
	}

	// finalSessionID tracks the chat session that actually ran the agent. It
	// defaults to the bound sessionID and is overwritten with freshSessionID
	// when the resume-failure retry path runs. CompleteTask forwards it to
	// the server so the task row's session_id is preserved (not NULLed) for the
	// next round's GetLastTaskSession lookup.
	finalSessionID := sessionID

	// runAgent supervises the agent boundary (ctx-cancel aware + best-effort
	// session abort on timeout) instead of trusting SessionRunner to return on
	// cancellation. execute stays the single owner of task-status callbacks.
	out, runErr := d.runAgent(ctx, payload, worktree, agentPath, sessionID)

	// Resume failure fallback: a resumed prior session that fails on first run
	// may be corrupt on disk — retry once with a fresh session. Skipped for
	// terminal ctx errors (timeout/cancel), where a fresh session cannot help
	// and the already-expired ctx would fail it instantly. Bounded to one retry.
	if runErr != nil && payload.PriorSessionID != "" && !d.aborted(payload.TaskID) &&
		!errors.Is(runErr, context.DeadlineExceeded) && !errors.Is(runErr, context.Canceled) {
		logger.Warn("workflow: resumed session failed (%v); retrying with fresh session", runErr)
		payload.PriorSessionID = "" // force bindSession to create a fresh chat session
		freshSessionID, bindErr := d.bindSession(ctx, payload, worktree)
		if bindErr != nil {
			return d.failTask(payload.TaskID, bindErr, "")
		}
		out, runErr = d.runAgent(ctx, payload, worktree, agentPath, freshSessionID)
		finalSessionID = freshSessionID
	}
	output := truncateOutput(string(out))
	// Stamp the finish time into .gc_meta.json so the artifact-only and
	// terminal TTLs anchor on when the task actually ended (covers both the
	// success and run-err paths below).
	writeGCMetaForTask(worktree, payload, time.Now().UTC())
	if runErr != nil {
		var taskErr error
		if d.aborted(payload.TaskID) {
			taskErr = d.failTask(payload.TaskID, fmt.Errorf("aborted: %w", runErr), "cancelled")
		} else if errors.Is(runErr, context.DeadlineExceeded) {
			taskErr = d.failTask(payload.TaskID, runErr, "agent_timeout")
		} else if errors.Is(runErr, agent.ErrEmptySessionOutput) {
			taskErr = d.failTask(payload.TaskID, runErr, "agent_empty_output")
		} else {
			taskErr = d.failTask(payload.TaskID, runErr, "agent_error")
		}
		// Terminal status is more important than supplemental output. Upload any
		// partial output only after FailTask so a slow messages endpoint cannot
		// leave the task visibly running.
		if output != "" {
			d.postTaskMessages(payload.TaskID, output)
		}
		return taskErr
	}
	if payload.Agent == AgentCsc && strings.TrimSpace(output) == "" {
		emptyErr := fmt.Errorf("%w: %s", agent.ErrEmptySessionOutput, sessionID)
		return d.failTask(payload.TaskID, emptyErr, "agent_empty_output")
	}
	d.postTaskMessages(payload.TaskID, output)

	logger.Info("workflow: task %s completed: session=%s output_bytes=%d", payload.TaskID, finalSessionID, len(output))

	return d.withTaskCallbackContext(func(callbackCtx context.Context) error {
		return d.client.CompleteTask(callbackCtx, payload.TaskID, output, finalSessionID, worktree)
	})
}

type agentRunResult struct {
	output []byte
	err    error
}

// runAgent supervises the agent boundary instead of trusting every SessionRunner
// implementation to return when its context is cancelled. The worker only
// produces a buffered result; execute remains the single owner of task status
// callbacks, so a late worker cannot complete a task that already timed out.
func (d *Driver) runAgent(ctx context.Context, payload workflow.TaskRunPayload, worktree, agentPath, sessionID string) ([]byte, error) {
	resultCh := make(chan agentRunResult, 1)
	go func() {
		var result agentRunResult
		if payload.Agent == AgentCsc && sessionID != "" && d.deps != nil && d.deps.SessionRunner != nil {
			result.output, result.err = d.runner.RunCSCSession(ctx, payload, worktree, sessionID)
		} else {
			result.output, result.err = d.runner.RunPrepared(ctx, payload, worktree, agentPath)
		}
		resultCh <- result
	}()

	select {
	case result := <-resultCh:
		if err := ctx.Err(); err != nil {
			d.abortSession(sessionID)
			return nil, err
		}
		return result.output, result.err
	case <-ctx.Done():
		d.abortSession(sessionID)
		return nil, ctx.Err()
	}
}

func (d *Driver) abortSession(sessionID string) {
	if sessionID == "" || d.deps == nil || d.deps.SessionRunner == nil {
		return
	}
	aborter, ok := d.deps.SessionRunner.(SessionAborter)
	if !ok {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), sessionAbortTimeout)
	defer cancel()

	resultCh := make(chan error, 1)
	go func() {
		resultCh <- aborter.AbortSession(ctx, sessionID)
	}()

	select {
	case err := <-resultCh:
		if err != nil {
			logger.Warn("workflow: abort session %s failed: %v", sessionID, err)
		}
	case <-ctx.Done():
		logger.Warn("workflow: abort session %s timed out: %v", sessionID, ctx.Err())
	}
}

func (d *Driver) withTaskCallbackContext(callback func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), taskCallbackTimeout)
	defer cancel()
	return callback(ctx)
}

func (d *Driver) postTaskMessages(taskID, output string) {
	if err := d.withTaskCallbackContext(func(ctx context.Context) error {
		return d.client.PostTaskMessages(ctx, taskID, output)
	}); err != nil {
		logger.Warn("workflow: task %s message callback failed: %v", taskID, err)
	}
}

func (d *Driver) failTask(taskID string, taskErr error, failureReason string) error {
	logger.Warn("workflow: task %s failed: reason=%s err=%v", taskID, failureReason, taskErr)
	callbackErr := d.withTaskCallbackContext(func(ctx context.Context) error {
		return d.client.FailTask(ctx, taskID, taskErr.Error(), failureReason)
	})
	if callbackErr != nil {
		return errors.Join(taskErr, fmt.Errorf("fail task callback: %w", callbackErr))
	}
	return taskErr
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
			return "", fmt.Errorf("server returned empty chat session id")
		}
		sessionID = session.ID
	}

	if d.deps.ConversationBinder != nil {
		// Create the csc session with the task env so in-task CLIs (notably
		// `cs-cloud workflow deliverable submit`, which needs CS_CLOUD_TOKEN +
		// CS_CLOUD_GITEA_* to push document deliverables to Gitea) inherit the
		// credentials the server pushed in the task payload. RunSession reuses
		// this session, so the env must be present at creation. Bind is fatal:
		// a failed local session must not proceed to remote pin/bind, which
		// would leave the task pointed at a session the frontend can't resolve.
		env := d.runner.buildEnv(payload, worktree)
		if err := d.deps.ConversationBinder.Bind(ctx, sessionID, worktree, env); err != nil {
			return "", fmt.Errorf("bind local conversation session: %w", err)
		}
	}

	// Pin the real workdir (task root) so the next round's prior_work_dir hits.
	if err := d.client.PinTaskSession(ctx, payload.TaskID, sessionID, worktree); err != nil {
		return "", fmt.Errorf("pin task session: %w", err)
	}
	if err := d.client.BindNodeRunSession(ctx, payload.NodeRunID, runtimeID, deviceID, sessionID); err != nil {
		return "", fmt.Errorf("bind node run session: %w", err)
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

// maintainRegistrations keeps the runtime rows for every workspace
// alive: register the missing ones, heartbeat the rest, and re-register any
// row the server dropped (heartbeat 404). Called once at startup and then on
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
// workspace if it has none. A 404 from heartbeat means the server deleted the
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
