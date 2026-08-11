package localserver

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"cs-cloud/internal/agent/csc"
	"cs-cloud/internal/config"
	"cs-cloud/internal/filewatcher"
	"cs-cloud/internal/gitwatcher"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/membertask"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/terminal"
	"cs-cloud/internal/updater"
	"cs-cloud/internal/workflowrunner"
)

type TunnelStatus struct {
	Connected   bool       `json:"connected"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

type TunnelStatusProvider interface {
	TunnelStatus() TunnelStatus
}

type PrewarmTracker interface {
	MarkStarted(dir string)
	MarkCompleted(dir string, err error)
}

type prewarmState struct {
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Error      string     `json:"error,omitempty"`
}

type Server struct {
	http    *http.Server
	ln      net.Listener
	url     string
	version string

	manager    *runtime.AgentManager
	eventBus   *runtime.EventBus
	termMgr    *terminal.TerminalManager
	termH      *terminal.Handlers
	inputWsH   *terminal.InputWsHandler
	runtimeCfg config.RuntimeConfig
	cfg        *config.Config
	rootDir    string
	recentMu   sync.Mutex

	findFilesMu     sync.Mutex
	findFilesCache  map[string]*fileSearchIndex
	findFilesBuilds map[string]*fileSearchBuild

	dispatcher *CommandDispatcher

	workflow *workflowrunner.Driver
	// workflowErr records why the workflow driver failed to start. Non-nil
	// means the subsystem is disabled but the daemon keeps serving its core
	// business (tunnel, agent proxy); workflow endpoints report it as
	// unavailable.
	workflowErr error

	tunnelStatus TunnelStatusProvider

	prewarmMu  sync.Mutex
	prewarmMap map[string]*prewarmState

	// Ring buffer backing GET /api/v1/runtime/events. Subscribes to
	// EventBus on Start, drains on Shutdown.
	ringBuffer  *RingBuffer
	tuiRegistry *TUIRegistry // Phase 1: nil stub; Phase 2: real instance

	// Post-upload attachment sweep debounce. Guards the lazy GC triggered by
	// handleAttachmentUpload so bursty uploads don't pay N× the scan cost.
	gcSweepMu   sync.Mutex
	lastGcSweep time.Time

	// Host event watchers
	fileWatcher *filewatcher.Watcher
	gitWatcher  *gitwatcher.Watcher

	updateChecker *updater.Checker

	// agentPIDWriter persists the current agent PID to disk so that
	// StopDaemon / ForceCleanupStale can locate the right process after
	// the agent has been restarted in-place. Optional; when nil, restart
	// paths skip persistence (used in tests).
	agentPIDWriter func(pid int)

	// adopter intercepts prompts sent to workflow-bound sessions and reopens
	// the associated task so the user's turn drives the workflow. Nil when the
	// workflow subsystem is unavailable.
	adopter *sessionAdopter

	memberTasks      *membertask.Service
	memberTaskSecret string
}

func New(opts ...Option) *Server {
	initStartTime()

	s := &Server{
		eventBus:    runtime.NewEventBus(),
		runtimeCfg:  defaultRuntimeConfig(),
		prewarmMap:  make(map[string]*prewarmState),
		tuiRegistry: NewTUIRegistry(),
	}
	s.manager = runtime.NewAgentManager(s.eventBus)
	for _, o := range opts {
		o(s)
	}
	s.ringBuffer = NewRingBuffer(s.eventBus)

	// Initialize host event watchers
	s.fileWatcher = filewatcher.New(s.eventBus)
	s.gitWatcher = gitwatcher.New(s.eventBus)

	s.termMgr = terminal.NewManager(terminal.WithConfig(s.cfg))
	s.termH = terminal.NewHandlers(s.termMgr)
	s.inputWsH = terminal.NewInputWsHandler(s.termMgr)

	mux := http.NewServeMux()
	api := http.NewServeMux()
	apiAuth := authMiddleware(apiKeyFromConfig(s.cfg))
	mux.Handle("/api/v1/", corsMiddleware(apiAuth(http.StripPrefix("/api/v1", api))))
	if s.memberTasks != nil {
		private := authMiddleware(s.memberTaskSecret)(s.requireLoopback(http.HandlerFunc(s.handleMemberTasks)))
		mux.Handle("/api/v1/member-tasks", private)
		mux.Handle("/api/v1/member-tasks/", private)
	}

	// CORS-friendly 404 for paths outside /api/v1/ (e.g. wrong baseUrl)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		setCORSHeaders(w.Header())
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.NotFound(w, r)
	})

	api.HandleFunc("GET /runtime/health", s.handleHealth)
	api.HandleFunc("GET /runtime/config", s.handleRuntimeConfig)
	api.HandleFunc("GET /runtime/files", s.handleFileList)
	api.HandleFunc("GET /runtime/files/meta", s.handleFileMeta)
	api.HandleFunc("GET /runtime/files/content", s.handleFileContent)
	// Removed 2026-08-03: PUT /runtime/files/content (handleFileWrite) was
	// deleted due to a path-traversal vulnerability — the handler honored
	// runtimeCfg.AllowAbsolutePaths (default true), which let any client
	// overwrite arbitrary existing files (e.g. ~/.ssh/authorized_keys,
	// /etc/hosts, Windows Startup folder) when the local server is exposed
	// via --host 0.0.0.0, and apiKey defaults to "" (no auth). It had no
	// in-tree consumers; binary uploads go through /attachment, and
	// workspace edits go through the editor flow. See git history for the
	// removed handler if a sandboxed replacement is ever needed.
	api.HandleFunc("GET /runtime/find/file", s.handleFindFiles)
	api.HandleFunc("GET /runtime/path", s.handlePath)
	api.HandleFunc("GET /runtime/vcs", s.handleVcs)
	api.HandleFunc("GET /runtime/diff", s.handleDiff)
	api.HandleFunc("GET /runtime/diff/content", s.handleDiffContent)
	api.HandleFunc("POST /runtime/dispose", s.handleInstanceDispose)
	api.HandleFunc("GET /runtime/init-status", s.handleInitStatus)
	api.HandleFunc("GET /runtime/update/check", s.handleUpdateCheck)
	api.HandleFunc("POST /runtime/update/apply", s.handleUpdateApply)

	// Public event ingress/egress for csc TUI and future local clients.
	// Ring-buffer backed; reply dispatch arrives in Phase 2.
	api.HandleFunc("POST /runtime/events", s.handleRuntimeEventPost)
	api.HandleFunc("GET /runtime/events", s.handleRuntimeEventList)

	api.HandleFunc("GET /openapi.json", s.handleOpenAPISpec)
	api.HandleFunc("GET /docs", s.handleSwaggerUI)
	api.HandleFunc("GET /docs/", s.handleSwaggerUI)

	api.HandleFunc("GET /agents", s.handleListAgents)
	api.HandleFunc("GET /agents/health", s.handleAgentHealth)
	api.HandleFunc("GET /agents/version", s.handleAgentVersion)
	api.HandleFunc("GET /agents/models", s.handleAgentModels)
	api.HandleFunc("GET /agents/session-modes", s.handleAgentSessionModes)
	api.HandleFunc("GET /agents/commands", s.handleCommands)
	api.HandleFunc("GET /agents/mcp", s.handleAgentMCP)
	api.HandleFunc("GET /agents/lsp", s.handleAgentLSP)
	api.HandleFunc("GET /agents/config", s.handleConfigGet)
	api.HandleFunc("PATCH /agents/config", s.handleConfigPatch)
	api.HandleFunc("GET /agents/models/config", s.handleProviderConfigGet)
	api.HandleFunc("PATCH /agents/models/config", s.handleProviderConfigPatch)

	api.HandleFunc("POST /conversations", s.handleConversationCreate)
	api.HandleFunc("GET /conversations", s.handleConversationList)
	api.HandleFunc("GET /conversations/status", s.handleConversationStatus)
	api.HandleFunc("GET /conversations/{id}", s.handleConversationGet)
	api.HandleFunc("PATCH /conversations/{id}", s.handleConversationUpdate)
	api.HandleFunc("DELETE /conversations/{id}", s.handleConversationDelete)
	api.HandleFunc("POST /conversations/{id}/prompt", s.handleConversationPrompt)
	api.HandleFunc("POST /conversations/{id}/prompt/async", s.handleConversationPromptAsync)
	api.HandleFunc("POST /conversations/{id}/abort", s.handleConversationAbort)
	api.HandleFunc("GET /conversations/{id}/messages", s.handleConversationMessages)
	api.HandleFunc("GET /conversations/{id}/todo", s.handleConversationTodo)
	api.HandleFunc("GET /conversations/{id}/tasks", s.handleConversationTasks)
	api.HandleFunc("GET /conversations/{id}/diff", s.handleConversationDiffDeprecated)
	api.HandleFunc("POST /conversations/{id}/shell", s.handleConversationShell)
	api.HandleFunc("POST /conversations/{id}/command", s.handleConversationCommand)
	api.HandleFunc("POST /conversations/{id}/command/async", s.handleConversationCommandAsync)
	api.HandleFunc("POST /conversations/{id}/revert", s.handleProxy)
	api.HandleFunc("POST /conversations/{id}/summarize", s.handleProxy)

	api.HandleFunc("GET /events", s.handleEvents)

	api.HandleFunc("POST /attachments", s.handleAttachmentUpload)
	api.HandleFunc("GET /attachments", s.handleAttachmentList)
	api.HandleFunc("GET /attachments/{id}", s.handleAttachmentGet)
	api.HandleFunc("DELETE /attachments", s.handleAttachmentGC)

	api.HandleFunc("GET /agents/favorites", s.handleFavoriteList)
	api.HandleFunc("POST /agents/favorites/{id}/load", s.handleFavoriteLoad)
	api.HandleFunc("POST /agents/favorites/{id}/unload", s.handleFavoriteUnload)

	api.HandleFunc("GET /permissions", s.handlePermissionList)
	api.HandleFunc("POST /permissions/{id}/reply", s.handlePermissionReply)

	api.HandleFunc("GET /questions", s.handleQuestionList)
	api.HandleFunc("POST /questions/{id}/reply", s.handleQuestionReply)
	api.HandleFunc("POST /questions/{id}/reject", s.handleQuestionReject)

	api.HandleFunc("DELETE /workspace", s.handleWorkspaceDelete)

	api.HandleFunc("POST /terminal", s.handleTerminalCreate)
	api.HandleFunc("DELETE /terminal/{id}", s.handleTerminalKill)
	api.HandleFunc("POST /terminal/{id}/resize", s.handleTerminalResize)
	api.HandleFunc("POST /terminal/{id}/restart", s.handleTerminalRestart)
	api.HandleFunc("GET /terminal/{id}/stream", s.handleTerminalStream)
	api.HandleFunc("POST /terminal/{id}/input", s.handleTerminalInput)
	api.HandleFunc("GET /terminal/input-ws", s.handleTerminalInputWS)

	api.HandleFunc("POST /commands", s.handleCommandDispatch)
	api.HandleFunc("GET /commands/status", s.handleCommandStatus)

	api.HandleFunc("GET /workflow/health", s.handleWorkflowHealth)
	api.HandleFunc("POST /workflow/tasks/{id}/run", s.handleWorkflowTaskRun)
	api.HandleFunc("POST /workflow/tasks/{id}/abort", s.handleWorkflowTaskAbort)
	api.HandleFunc("POST /workflow/tasks/{id}/complete", s.handleWorkflowTaskComplete)
	api.HandleFunc("GET /workflow/facts", s.handleWorkflowTaskFacts)

	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s
}

type Option func(*Server)

func WithVersion(v string) Option {
	return func(s *Server) { s.version = v }
}

// apiKeyFromConfig pulls the optional shared request key from the configured
// Config. Empty string means "no auth" (the default).
func apiKeyFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.APIKey
}

func WithRuntimeConfig(cfg config.RuntimeConfig) Option {
	return func(s *Server) { s.runtimeCfg = cfg }
}

func WithConfig(cfg *config.Config) Option {
	return func(s *Server) { s.cfg = cfg }
}

func WithRootDir(dir string) Option {
	return func(s *Server) { s.rootDir = dir }
}

func WithWorkflow(d *workflowrunner.Driver) Option {
	return func(s *Server) {
		s.workflow = d
	}
}

func WithMemberTask(service *membertask.Service, secret string) Option {
	return func(s *Server) {
		s.memberTasks = service
		s.memberTaskSecret = secret
	}
}

func (s *Server) Manager() *runtime.AgentManager {
	return s.manager
}

func (s *Server) EventBus() *runtime.EventBus {
	return s.eventBus
}

func (s *Server) Start(addr string) error {
	if s.memberTasks != nil && s.memberTaskSecret == "" {
		return fmt.Errorf("member task private secret is empty")
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln

	// Normalize the URL: wildcard addresses like 0.0.0.0 or :: are not
	// directly usable in a browser or API client. Replace them with
	// 127.0.0.1 so the reported URL is always accessible locally.
	host, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		s.url = "http://" + ln.Addr().String()
	} else if host == "0.0.0.0" || host == "::" {
		s.url = "http://127.0.0.1:" + port
	} else {
		s.url = "http://" + ln.Addr().String()
	}

	// Start host event watchers
	ctx := context.Background()
	if s.fileWatcher != nil {
		if err := s.fileWatcher.Start(ctx); err != nil {
			logger.Error("Failed to start file watcher: %v", err)
		}
	}
	if s.gitWatcher != nil {
		if err := s.gitWatcher.Start(ctx, s.fileWatcher); err != nil {
			logger.Error("Failed to start git watcher: %v", err)
		}
	}

	if s.workflow != nil {
		// s.url is known now (listener bound); inject it so in-task CLIs can
		// call back into this device's /workflow/tasks/{id}/complete endpoint.
		s.workflow.SetLocalBaseURL(s.url)
		// Pass the localserver API key so those callbacks authenticate when
		// apiAuth is enabled (no-op when no key is configured).
		s.workflow.SetLocalAPIKey(apiKeyFromConfig(s.cfg))
		// Hand the runtime EventBus to the driver so AdoptUserTurn can
		// subscribe to an adopted session's events. The bus is constructed
		// above (before this driver), so wire it now, before Start.
		s.workflow.SetEventBus(s.eventBus)
		if err := s.workflow.Start(); err != nil {
			// The workflow subsystem is optional. A daemon registered
			// against a server without the workflow backend (no server
			// base URL) must still come up — degrade to "workflow
			// disabled" instead of failing the whole server.
			logger.Warn("workflow driver disabled: %v", err)
			s.workflowErr = err
		} else {
			s.adopter = &sessionAdopter{
				bindings: s.workflow.Client(),
				driver:   s.workflow,
				resolve:  s.resolveCSCAgent,
			}
		}
	}

	// Start the ring buffer drain goroutine (subscribes to EventBus).
	if s.ringBuffer != nil {
		s.ringBuffer.Start(ctx)
	}
	// Start the TUI registry's TTL cleanup goroutine (ticks every minute).
	// Without this, expired permission/question entries leak until the
	// process restarts; the dispatcher's expiry-aware reads still work,
	// but re-registration of a TTL'd id would waste a map slot.
	if s.tuiRegistry != nil {
		s.tuiRegistry.Start(ctx)
	}

	go func() {
		_ = s.http.Serve(ln)
	}()
	return nil
}

func (s *Server) URL() string {
	return s.url
}

func (s *Server) Port() int {
	if s.ln == nil {
		return 0
	}
	return s.ln.Addr().(*net.TCPAddr).Port
}

func (s *Server) Shutdown(ctx context.Context) error {
	// Stop host event watchers
	if s.gitWatcher != nil {
		s.gitWatcher.Stop()
	}
	if s.fileWatcher != nil {
		s.fileWatcher.Stop()
	}
	// Stop the ring buffer drain goroutine before tearing down the bus.
	if s.ringBuffer != nil {
		s.ringBuffer.Stop()
	}
	// Stop the TUI registry's cleanup goroutine.
	if s.tuiRegistry != nil {
		s.tuiRegistry.Stop()
	}

	if s.workflow != nil {
		if err := s.workflow.Stop(); err != nil {
			logger.Error("Failed to stop workflow driver: %v", err)
		}
	}

	s.manager.KillAll()
	s.termMgr.CloseAll()
	return s.http.Shutdown(ctx)
}

// resolveCSCAgent returns the csc agent bound to the given session — the same
// agent the proxy forwards that session's prompts to.
//
// Only an agent registered for this exact session qualifies. There is
// deliberately no fallback to a default/shared csc agent: an adopted turn
// subscribes to that agent's session event stream, and a different agent that
// does not own the session would emit no busy/idle events for it, so the turn
// would hang silently until agent_timeout. If the session has no bound agent,
// adoption is skipped and the prompt falls through as an ordinary conversation.
func (s *Server) resolveCSCAgent(sessionID string) (*csc.Agent, error) {
	a, ok := s.manager.GetAgent(sessionID)
	if !ok {
		return nil, fmt.Errorf("no agent bound to session %s", sessionID)
	}
	cscAgent, ok := a.(*csc.Agent)
	if !ok {
		return nil, fmt.Errorf("agent bound to session %s is not a csc agent", sessionID)
	}
	return cscAgent, nil
}

func (s *Server) TerminalManager() *terminal.TerminalManager {
	return s.termMgr
}

func (s *Server) SetDispatcher(d *CommandDispatcher) {
	s.dispatcher = d
}

func (s *Server) Dispatcher() *CommandDispatcher {
	return s.dispatcher
}

func (s *Server) SetTunnelStatusProvider(p TunnelStatusProvider) {
	s.tunnelStatus = p
}

// SetAgentPIDWriter registers a callback invoked whenever the active agent
// PID changes (initial start, RestartDefaultAgent, /runtime/dispose). Pass
// nil to clear. The callback receives 0 when no agent is alive.
func (s *Server) SetAgentPIDWriter(fn func(pid int)) {
	s.agentPIDWriter = fn
}

// persistAgentPID is a no-op when no writer is registered.
func (s *Server) persistAgentPID() {
	if s.agentPIDWriter == nil {
		return
	}
	s.agentPIDWriter(s.manager.AgentPID())
}

func (s *Server) MarkStarted(dir string) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	now := time.Now()
	st := s.prewarmMap[dir]
	if st == nil {
		st = &prewarmState{}
		s.prewarmMap[dir] = st
	}
	st.Status = "in_progress"
	st.StartedAt = &now
	st.FinishedAt = nil
	st.Error = ""
}

func (s *Server) MarkCompleted(dir string, err error) {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	now := time.Now()
	st := s.prewarmMap[dir]
	if st == nil {
		st = &prewarmState{}
		s.prewarmMap[dir] = st
	}
	if err != nil {
		st.Status = "failed"
		st.Error = err.Error()
	} else {
		st.Status = "completed"
	}
	st.FinishedAt = &now
}

func (s *Server) GetPrewarmState(dir string) *prewarmState {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	st := s.prewarmMap[dir]
	if st == nil {
		return nil
	}
	cp := *st
	if st.StartedAt != nil {
		t := *st.StartedAt
		cp.StartedAt = &t
	}
	if st.FinishedAt != nil {
		t := *st.FinishedAt
		cp.FinishedAt = &t
	}
	return &cp
}

func (s *Server) TriggerPrewarmIfNeeded(dir string) {
	s.prewarmMu.Lock()
	if _, exists := s.prewarmMap[dir]; exists {
		s.prewarmMu.Unlock()
		return
	}
	now := time.Now()
	s.prewarmMap[dir] = &prewarmState{
		Status:    "in_progress",
		StartedAt: &now,
	}
	s.prewarmMu.Unlock()

	go s.prewarmDir(context.Background(), dir)
}

func (s *Server) prewarmDir(ctx context.Context, dir string) {
	base := s.manager.Endpoint()
	if base == "" {
		s.MarkCompleted(dir, fmt.Errorf("agent endpoint not available"))
		return
	}

	s.prewarmRequest(ctx, &http.Client{Timeout: 30 * time.Second}, base, "/session", dir)

	paths := s.manager.PrewarmPaths()
	if len(paths) == 0 {
		s.MarkCompleted(dir, nil)
		return
	}

	var wg sync.WaitGroup
	for _, path := range paths {
		path := path
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.prewarmRequest(ctx, &http.Client{Timeout: 15 * time.Second}, base, path, dir)
		}()
	}
	wg.Wait()
	s.MarkCompleted(dir, nil)
}

func (s *Server) prewarmRequest(ctx context.Context, cli *http.Client, base string, path string, dir string) {
	begin := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		logger.Warn("prewarm request build failed (%s): %v", path, err)
		return
	}
	if hdr := s.manager.WorkspaceHeaderName(); hdr != "" {
		req.Header.Set(hdr, dir)
	}

	resp, err := cli.Do(req)
	if err != nil {
		logger.Warn("prewarm request failed (%s) after %s: %v", path, time.Since(begin), err)
		return
	}
	resp.Body.Close()

	cost := time.Since(begin)
	if resp.StatusCode >= http.StatusBadRequest {
		logger.Warn("prewarm request returned %d (%s) in %s", resp.StatusCode, path, cost)
		return
	}
	logger.Info("prewarm request ok (%s) in %s", path, cost)
}

func (s *Server) AllPrewarmStates() map[string]*prewarmState {
	s.prewarmMu.Lock()
	defer s.prewarmMu.Unlock()
	out := make(map[string]*prewarmState, len(s.prewarmMap))
	for dir, st := range s.prewarmMap {
		cp := *st
		if st.StartedAt != nil {
			t := *st.StartedAt
			cp.StartedAt = &t
		}
		if st.FinishedAt != nil {
			t := *st.FinishedAt
			cp.FinishedAt = &t
		}
		out[dir] = &cp
	}
	return out
}

// WatchDirectory starts watching a directory for file system changes
func (s *Server) WatchDirectory(dir string) error {
	if s.fileWatcher == nil {
		return fmt.Errorf("file watcher not initialized")
	}
	return s.fileWatcher.AddDirectory(dir)
}

// UnwatchDirectory stops watching a directory
func (s *Server) UnwatchDirectory(dir string) error {
	if s.fileWatcher == nil {
		return fmt.Errorf("file watcher not initialized")
	}
	return s.fileWatcher.RemoveDirectory(dir)
}

// WatchGitRepo starts watching a git repository
func (s *Server) WatchGitRepo(repoPath string) error {
	if s.gitWatcher == nil {
		return fmt.Errorf("git watcher not initialized")
	}
	return s.gitWatcher.AddRepository(repoPath)
}

// UnwatchGitRepo stops watching a git repository
func (s *Server) UnwatchGitRepo(repoPath string) error {
	if s.gitWatcher == nil {
		return fmt.Errorf("git watcher not initialized")
	}
	return s.gitWatcher.RemoveRepository(repoPath)
}

// WatchingDirectories returns the list of directories being watched
func (s *Server) WatchingDirectories() []string {
	if s.fileWatcher == nil {
		return nil
	}
	return s.fileWatcher.WatchingDirectories()
}

// WatchingGitRepos returns the list of git repositories being watched
func (s *Server) WatchingGitRepos() []string {
	if s.gitWatcher == nil {
		return nil
	}
	return s.gitWatcher.WatchingRepositories()
}
