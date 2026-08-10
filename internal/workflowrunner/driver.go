package workflowrunner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/platform"
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

// SessionPermissionBypass is the permission mode used for workflow-bound
// conversation sessions. Workflow tasks run unattended, so nobody would
// answer permission.asked prompts; sessions must not wait for approval.
const SessionPermissionBypass = "bypassPermissions"

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
	// completion holds the explicit-completion state for running csc tasks,
	// keyed by task ID. When the agent invokes the "complete task" tool, the
	// localserver handler calls SignalTaskCompletion, which stores the payload
	// and closes the notify channel so runAgent can stop the session and report
	// success via the normal CompleteTask path. execute remains the sole owner
	// of task-status callbacks.
	completion map[string]*completionState
	// outbox is the durable task-fact outbox. Populated by Task 2.3; delivery
	// loop methods tolerate a nil outbox and simply do nothing.
	outbox *Outbox
	// runCtx/runCancel bound the driver's background goroutines (outbox
	// delivery). Created on Start and cancelled on Stop.
	runCtx    context.Context
	runCancel context.CancelFunc
	// localBaseURL is this device's localserver URL, applied to the task
	// runner on Start so in-task CLIs can call back into the driver. Set by
	// the localserver (which knows its URL only after binding its listener).
	localBaseURL string
	// localAPIKey is the localserver API key (empty when none configured),
	// applied to the task runner on Start so in-task completion callbacks
	// authenticate to the localserver's apiAuth middleware. Set by the
	// localserver alongside SetLocalBaseURL.
	localAPIKey string
	// adoptEstablishmentTimeout bounds the SSE subscription setup phase in
	// AdoptUserTurn. It defaults to defaultAdoptEstablishmentTimeout and is
	// exposed only for tests.
	adoptEstablishmentTimeout time.Duration
	mu                        sync.Mutex
}

// NewDriver creates a new workflow driver.
func NewDriver(cfg workflow.Config, deps *Dependencies) *Driver {
	return &Driver{cfg: cfg, deps: deps}
}

// Name returns the driver name.
func (d *Driver) Name() string { return "workflow" }

// Client returns the multica REST client used by the driver. Available after
// Start has succeeded; nil before that.
func (d *Driver) Client() *Client {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client
}

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
	if d.adoptEstablishmentTimeout <= 0 {
		d.adoptEstablishmentTimeout = defaultAdoptEstablishmentTimeout
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
	// Inject the localserver URL so in-task CLIs (cs-cloud workflow task
	// complete) can call back into this device's driver via CS_CLOUD_LOCAL_URL.
	d.runner.SetLocalServerURL(d.localBaseURL)
	// Inject the localserver API key so those callbacks authenticate when the
	// localserver has apiAuth enabled.
	d.runner.SetLocalAPIKey(d.localAPIKey)
	d.sem = make(chan struct{}, d.cfg.MaxConcurrentTasks)
	d.running = make(map[string]*taskRecord)
	d.abortedIDs = make(map[string]time.Time)
	d.registrations = make(map[string]string)
	// The outbox lives under the app dir so it survives daemon restarts and
	// rebinds to the same directory after upgrade.
	d.outbox = NewOutbox(platform.AppDir())

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
	d.runCtx, d.runCancel = context.WithCancel(context.Background())
	go d.startOutboxDelivery(d.runCtx)
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
	// Cancel the driver's run context so background goroutines such as the
	// outbox delivery loop return promptly.
	if d.runCancel != nil {
		d.runCancel()
		d.runCancel = nil
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

// IsTaskRunning reports whether the task is currently in the running table.
// It is safe to call from handlers outside of task execution goroutines.
func (d *Driver) IsTaskRunning(taskID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.running[taskID]
	return ok
}

// Outbox returns the driver's durable task-fact outbox. It is always non-nil
// after Start has succeeded.
func (d *Driver) Outbox() *Outbox {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.outbox
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
	return d.reserveTaskID(payload.TaskID)
}

// reserveTaskID is the taskID-only variant used by AdoptUserTurn, which does
// not have a full task payload. The semaphore, running map, and abort
// tombstone logic match reserve exactly.
func (d *Driver) reserveTaskID(taskID string) (*taskRecord, error) {
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
	if _, exists := d.running[taskID]; exists {
		<-d.sem
		return nil, ErrTaskAlreadyRunning
	}
	if ts, wasAborted := d.abortedIDs[taskID]; wasAborted {
		<-d.sem
		if time.Since(ts) <= abortTombstoneTTL {
			delete(d.abortedIDs, taskID)
			return nil, fmt.Errorf("task %s was aborted before it started", taskID)
		}
		delete(d.abortedIDs, taskID)
	}
	// GC expired tombstones while we hold the lock.
	for id, ts := range d.abortedIDs {
		if time.Since(ts) > abortTombstoneTTL {
			delete(d.abortedIDs, id)
		}
	}

	rec := &taskRecord{}
	d.running[taskID] = rec
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
	worktree = d.alignResumedSessionWorkdir(ctx, payload, worktree)
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
	if len(payload.Repos) > 0 {
		logger.Info("workflow: task %s repo downloads expected: %s", payload.TaskID, repoSummary(payload.Repos, payload.RepoURL))
	}

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
	if payload.Agent == AgentCsc && sessionID != "" {
		// Enable the explicit "complete task" tool path for bound csc sessions:
		// the localserver handler signals completion via this registry while
		// runAgent waits on the session.
		d.registerCompletion(payload.TaskID, sessionID, worktree)
		defer d.unregisterCompletion(payload.TaskID)
	}
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
	if observations := observeRepoDownloads(worktree, payload.Repos); len(observations) > 0 {
		summary := repoDownloadObservationSummary(observations)
		if repoDownloadObservationHasMissing(observations) {
			logger.Warn("workflow: task %s repo downloads observed: %s", payload.TaskID, summary)
		} else {
			logger.Info("workflow: task %s repo downloads observed: %s", payload.TaskID, summary)
		}
	}
	if runErr != nil {
		var taskErr error
		if d.aborted(payload.TaskID) {
			taskErr = d.failTask(payload.TaskID, fmt.Errorf("aborted: %w", runErr), "cancelled")
		} else if errors.Is(runErr, agent.ErrIncomplete) {
			taskErr = d.failTask(payload.TaskID, runErr, "agent_incomplete")
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
	// Explicit completion (agent invoked the complete tool): use the tool's
	// payload as the task output / decision instead of the session stdout.
	if sig, ok := d.popCompletionSignal(payload.TaskID); ok {
		output := truncateOutput(sig.Summary)
		if strings.TrimSpace(output) == "" {
			output = strings.TrimSpace(truncateOutput(string(out)))
		}
		// A critic's review signal carries its payload in Decision/Reason, not
		// Summary — and the abort path above discards session stdout. Use the
		// reason as the output, and never misjudge a decision-carrying signal
		// as empty output: the decision alone is a valid completion.
		if strings.TrimSpace(output) == "" {
			output = truncateOutput(sig.Reason)
		}
		if payload.Agent == AgentCsc && strings.TrimSpace(output) == "" && sig.Decision == "" {
			emptyErr := fmt.Errorf("%w: %s", agent.ErrEmptySessionOutput, sessionID)
			return d.failTask(payload.TaskID, emptyErr, "agent_empty_output")
		}
		d.postTaskMessages(payload.TaskID, output)
		logger.Info("workflow: task %s completed via tool: session=%s action=%s decision=%s",
			payload.TaskID, finalSessionID, sig.Action, sig.Decision)
		return d.withTaskCallbackContext(func(callbackCtx context.Context) error {
			return d.completeTaskOrFailOnRejection(callbackCtx, payload.TaskID, output, finalSessionID, worktree, sig)
		})
	}
	if payload.Agent == AgentCsc && strings.TrimSpace(output) == "" {
		emptyErr := fmt.Errorf("%w: %s", agent.ErrEmptySessionOutput, sessionID)
		return d.failTask(payload.TaskID, emptyErr, "agent_empty_output")
	}
	d.postTaskMessages(payload.TaskID, output)

	logger.Info("workflow: task %s completed: session=%s output_bytes=%d", payload.TaskID, finalSessionID, len(output))

	return d.withTaskCallbackContext(func(callbackCtx context.Context) error {
		return d.completeTaskOrFailOnRejection(callbackCtx, payload.TaskID, output, finalSessionID, worktree, agent.CompletionSignal{})
	})
}

func (d *Driver) completeTaskOrFailOnRejection(ctx context.Context, taskID, output, sessionID, worktree string, sig agent.CompletionSignal) error {
	err := d.client.CompleteTask(ctx, taskID, output, sessionID, worktree, sig)
	if err == nil {
		return nil
	}
	var statusErr *StatusError
	if errors.As(err, &statusErr) && completionRejectionStatus(statusErr.StatusCode) {
		reason := strings.TrimSpace(statusErr.Body)
		if reason == "" {
			reason = err.Error()
		}
		failErr := d.client.FailTask(ctx, taskID, "completion rejected by server: "+reason, "completion_rejected")
		if failErr != nil {
			return fmt.Errorf("%w; fail rejected completion: %v", err, failErr)
		}
	}
	return err
}

func completionRejectionStatus(status int) bool {
	return status == http.StatusBadRequest
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
	// Pure-tool completion applies only to bound csc sessions — the only runs
	// where the agent has a "complete task" tool. notify is non-nil iff execute
	// registered a completion state for this task.
	pureTool := payload.Agent == AgentCsc && sessionID != "" && d.deps != nil && d.deps.SessionRunner != nil
	notify := d.completionNotify(payload.TaskID)

	resultCh := make(chan agentRunResult, 1)
	go func() {
		var result agentRunResult
		if pureTool {
			result.output, result.err = d.runner.RunCSCSession(ctx, payload, worktree, sessionID)
		} else {
			result.output, result.err = d.runner.RunPrepared(ctx, payload, worktree, agentPath)
		}
		resultCh <- result
	}()

	select {
	case <-notify:
		// The agent invoked the explicit complete tool mid-session. Stop the
		// session and report success; execute reads the payload from the
		// driver's completion registry.
		d.abortSession(sessionID)
		select {
		case <-resultCh: // drain; the aborted RunCSCSession result is irrelevant
		case <-time.After(sessionAbortTimeout):
			logger.Warn("workflow: session %s did not exit after abort", sessionID)
		}
		return nil, nil
	case result := <-resultCh:
		if err := ctx.Err(); err != nil {
			d.abortSession(sessionID)
			return nil, err
		}
		if result.err != nil {
			return result.output, result.err
		}
		if pureTool {
			// The csc session ended its turn cleanly. A completion signal that
			// already arrived before the session returned is treated as complete.
			select {
			case <-notify:
				return nil, nil
			default:
			}
			// A clean idle is not a completion. Any complete signal that arrives
			// after the session has ended is persisted to the outbox by
			// SignalTaskCompletion; the server's ApplyTaskFacts resurrects a
			// failed task into completed when the complete fact falls inside the
			// server's grace window. The device no longer waits here.
			return nil, agent.ErrIncomplete
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
	// Durability first: persist the failure fact before any in-process failure
	// handling so a crash between here and the server callback can be retried.
	d.writeFailFactToOutbox(taskID, taskErr, failureReason)
	// The failure path never pops the completion registry; the failure can be a
	// misjudgment (the agent may still be alive and finish its work). Mark the
	// entry failed so any completion signal arriving before execute returns is
	// persisted to the outbox instead of being dropped. The server arbitrates
	// the fail/complete race.
	d.mu.Lock()
	if cs, ok := d.completion[taskID]; ok {
		cs.failed = true
	}
	d.mu.Unlock()
	callbackErr := d.withTaskCallbackContext(func(ctx context.Context) error {
		return d.client.FailTask(ctx, taskID, taskErr.Error(), failureReason)
	})
	if callbackErr != nil {
		// Log here, not just in the returned error: RunTaskAsync discards
		// execute's return value, so an unlogged callback failure would leave
		// the server thinking the task is still running with no trace.
		logger.Warn("workflow: task %s fail callback failed: %v", taskID, callbackErr)
		return errors.Join(taskErr, fmt.Errorf("fail task callback: %w", callbackErr))
	}
	return taskErr
}

// writeFailFactToOutbox persists a "fail" task fact durably. Failures are
// logged loudly but do not block the in-process failure path.
func (d *Driver) writeFailFactToOutbox(taskID string, taskErr error, failureReason string) {
	if d.outbox == nil {
		return
	}
	factID, err := newFactID()
	if err != nil {
		logger.Error("workflow: task %s failed to generate fail fact id: %v", taskID, err)
		return
	}
	errStr := ""
	if taskErr != nil {
		errStr = taskErr.Error()
	}
	fact := OutboxFact{
		FactID:        factID,
		TaskID:        taskID,
		Kind:          "fail",
		OccurredAt:    time.Now().UTC(),
		Error:         errStr,
		FailureReason: failureReason,
	}
	if err := d.outbox.Add(fact); err != nil {
		logger.Error("workflow: task %s fail fact write-through failed: %v", taskID, err)
	}
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
		if err := d.deps.ConversationBinder.Bind(ctx, sessionID, worktree, env, SessionPermissionBypass); err != nil {
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

// alignResumedSessionWorkdir points the task workdir at the resumed session's
// actual cwd. A resumed csc session keeps the cwd of the task that created it
// (typically the previous phase's task dir), while the server does not always
// send prior_work_dir for the new task — Prepare then returns a fresh dir and
// the in-task CLI, which resolves CS_CLOUD_TASK_ID from <cwd>/.cs-cloud.env,
// would signal the previous, already-finished task (observed as a critic's
// approve 409ing against the completed worker task, after which the run was
// failed as agent_incomplete/agent_empty_output). Aligning the workdir makes
// writeTaskEnvFile refresh the env file where the session actually runs.
//
// The session's cwd is the ground truth, so this runs even when
// prior_work_dir is set: a pin recorded during a mismatched run would
// otherwise perpetuate the wrong dir into every retry. Unknown sessions (csc
// serve restarted and dropped its store) keep the prepared workdir — the run
// recreates the session there with the current env.
func (d *Driver) alignResumedSessionWorkdir(ctx context.Context, payload workflow.TaskRunPayload, worktree string) string {
	if payload.Agent != AgentCsc || payload.PriorSessionID == "" {
		return worktree
	}
	if d.deps == nil || d.deps.SessionRunner == nil {
		return worktree
	}
	resolver, ok := d.deps.SessionRunner.(SessionDirectoryResolver)
	if !ok {
		return worktree
	}
	dir, err := resolver.SessionDirectory(ctx, payload.PriorSessionID)
	if err != nil {
		logger.Warn("workflow: task %s resume: resolve session %s cwd failed, keeping prepared workdir: %v",
			payload.TaskID, payload.PriorSessionID, err)
		return worktree
	}
	if dir == "" || !dirExists(dir) {
		return worktree
	}
	if filepath.Clean(dir) == filepath.Clean(worktree) {
		return worktree
	}
	logger.Info("workflow: task %s resume: aligning workdir to session %s cwd %s (prepared %s)",
		payload.TaskID, payload.PriorSessionID, dir, worktree)
	return dir
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

// SetLocalBaseURL injects this device's localserver URL so in-task CLIs can
// call back into the driver. Must be called before Start so the task runner
// picks it up; the localserver sets it once it has bound its listener.
func (d *Driver) SetLocalBaseURL(url string) {
	d.localBaseURL = url
}

// SetLocalAPIKey injects the localserver API key so in-task completion
// callbacks authenticate to the localserver's apiAuth middleware. Must be
// called before Start; empty means the localserver has no API key configured.
func (d *Driver) SetLocalAPIKey(key string) {
	d.localAPIKey = key
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
