package csc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

const CLIBinary = "csc"

type Agent struct {
	mu    sync.Mutex
	id    string
	state agent.AgentState

	command     agent.Command
	workDir     string
	customEnv   map[string]string
	endpoint    string
	rawEndpoint string
	cmd         *exec.Cmd
	waitCh      chan error
	cancel      context.CancelFunc
	adapter     *AdapterServer

	sessionID    string
	modelInfo    *agent.ModelInfo
	eventEmitter func(agent.Event)

	httpClient *http.Client
}

func NewAgent(cfg agent.AgentConfig) *Agent {
	cmd := agent.ParseCommand(CLIBinary + " serve")
	if extra := cfg.Extra; extra != nil {
		if c, ok := extra["command"].(agent.Command); ok && !c.IsZero() {
			cmd = c
		}
	}
	return &Agent{
		id:        cfg.ID,
		command:   cmd,
		workDir:   cfg.WorkingDir,
		customEnv: cfg.CustomEnv,
		state:     agent.StateIdle,
		httpClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (a *Agent) ID() string      { return a.id }
func (a *Agent) Backend() string { return "csc" }
func (a *Agent) Driver() string  { return "http" }
func (a *Agent) PID() int {
	if a.cmd != nil && a.cmd.Process != nil {
		return a.cmd.Process.Pid
	}
	return 0
}
func (a *Agent) SessionID() string { return a.sessionID }
func (a *Agent) Endpoint() string  { return a.endpoint }
func (a *Agent) State() agent.AgentState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *Agent) SetEventEmitter(emitter func(agent.Event)) {
	a.eventEmitter = emitter
}

func (a *Agent) setState(s agent.AgentState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.state = s
}

func (a *Agent) emit(event agent.Event) {
	if a.eventEmitter != nil {
		a.eventEmitter(event)
	}
}

func (a *Agent) commandDisplay() string {
	return strings.Join(a.command.Args, " ")
}

func (a *Agent) Start(ctx context.Context) error {
	a.setState(agent.StateConnecting)

	agentCtx, agentCancel := context.WithCancel(ctx)
	a.cancel = agentCancel

	logger.Info("[debug] spawning '%s' and waiting for port...", a.commandDisplay())
	begin := time.Now()
	endpoint, err := a.spawnAndWaitForPort(agentCtx)
	logger.Info("[debug] spawnAndWaitForPort took %s, err=%v", time.Since(begin), err)
	if err != nil {
		a.setState(agent.StateError)
		a.cancel = nil
		agentCancel()
		return fmt.Errorf("failed to start agent '%s': %w", a.commandDisplay(), err)
	}
	a.rawEndpoint = endpoint
	logger.Info("csc raw endpoint resolved: %s", a.rawEndpoint)

	adapter, err := NewAdapterServer(a.rawEndpoint)
	if err != nil {
		a.setState(agent.StateError)
		a.Kill()
		return fmt.Errorf("failed to start csc adapter: %w", err)
	}
	a.adapter = adapter
	a.endpoint = adapter.URL()
	logger.Info("csc adapter endpoint resolved: %s", a.endpoint)

	resp, err := a.doRawGet(agentCtx, "/health")
	if err != nil {
		a.setState(agent.StateError)
		a.Kill()
		return fmt.Errorf("csc health check failed: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.setState(agent.StateError)
		a.Kill()
		return fmt.Errorf("csc health check returned status %d", resp.StatusCode)
	}

	a.setState(agent.StateConnected)

	go a.subscribeEvents(agentCtx)

	return nil
}

func (a *Agent) Kill() error {
	a.setState(agent.StateDisconnected)

	if a.cancel != nil {
		a.cancel()
		a.cancel = nil
	}

	if a.cmd != nil && a.cmd.Process != nil {
		a.gracefulShutdown(5 * time.Second)
	}
	if a.httpClient != nil {
		a.httpClient.CloseIdleConnections()
	}
	if a.adapter != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = a.adapter.Close(ctx)
		cancel()
		a.adapter = nil
	}
	return nil
}

func (a *Agent) gracefulShutdown(timeout time.Duration) {
	if a.endpoint != "" && a.httpClient != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/global/dispose", nil)
		if req != nil {
			resp, err := a.httpClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
		cancel()
	}

	agent.SignalTerminate(a.cmd.Process.Pid)

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if a.waitCh == nil {
			return
		}
		select {
		case <-a.waitCh:
			a.waitCh = nil
			return
		case <-time.After(100 * time.Millisecond):
		}
	}

	agent.KillProcessTree(a.cmd.Process.Pid)
	if a.waitCh != nil {
		<-a.waitCh
		a.waitCh = nil
	}
}

func (a *Agent) SendMessage(ctx context.Context, msg agent.PromptMessage) error {
	if a.sessionID == "" {
		return fmt.Errorf("no active session")
	}
	body := map[string]any{
		"content": msg.Content,
		"files":   msg.Files,
	}
	_, err := a.doPost(ctx, "/session/"+a.sessionID+"/prompt_async", body)
	if err != nil {
		return fmt.Errorf("send prompt failed: %w", err)
	}
	return nil
}

func (a *Agent) CancelPrompt(ctx context.Context) error {
	if a.sessionID == "" {
		return fmt.Errorf("no active session")
	}
	_, err := a.doPost(ctx, "/session/"+a.sessionID+"/abort", nil)
	return err
}

func (a *Agent) ConfirmPermission(ctx context.Context, callID string, optionID string) error {
	if a.sessionID == "" {
		return fmt.Errorf("no active session")
	}
	_, err := a.doRawPost(ctx, "/permission/"+callID+"/reply", map[string]any{"behavior": optionID})
	return err
}

func (a *Agent) PendingPermissions() []agent.PermissionInfo { return nil }

func (a *Agent) GetModelInfo() *agent.ModelInfo { return a.modelInfo }

func (a *Agent) SetModel(ctx context.Context, modelID string) (*agent.ModelInfo, error) {
	return a.modelInfo, nil
}

func (a *Agent) spawnAndWaitForPort(ctx context.Context) (string, error) {
	args := a.command.Args
	displayName := a.commandDisplay()

	execCmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if a.workDir != "" {
		execCmd.Dir = a.workDir
	}
	env := append(os.Environ(), "CSC_DISABLE_EMBEDDED_WEB_UI=1")
	for k, v := range a.customEnv {
		env = append(env, k+"="+v)
	}
	execCmd.Env = env
	agent.SetCmdProcessGroup(execCmd)

	stdout, err := execCmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}

	stderrPipe, err := execCmd.StderrPipe()
	if err != nil {
		return "", fmt.Errorf("stderr pipe: %w", err)
	}

	if err := execCmd.Start(); err != nil {
		return "", fmt.Errorf("start %s: %w", displayName, err)
	}

	a.cmd = execCmd
	a.waitCh = make(chan error, 1)
	go func() { a.waitCh <- execCmd.Wait() }()

	endpointCh := make(chan string, 1)
	errCh := make(chan error, 1)

	scanOutput := func(r io.Reader, tag string) {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			logger.Info("[%s] %s: %s", tag, displayName, line)
			for _, pat := range agent.PortPatterns {
				matches := pat.FindStringSubmatch(line)
				if len(matches) >= 2 {
					select {
					case endpointCh <- "http://127.0.0.1:" + matches[1]:
					default:
					}
					if tag == "stderr" {
						continue
					}
					return
				}
			}
		}
	}

	go scanOutput(stdout, "stdout")
	go scanOutput(stderrPipe, "stderr")

	timeout := time.After(30 * time.Second)
	select {
	case ep := <-endpointCh:
		return ep, nil
	case err := <-errCh:
		return "", err
	case <-a.waitCh:
		return "", fmt.Errorf("%s exited unexpectedly (no matching port output)", displayName)
	case <-timeout:
		_ = execCmd.Process.Kill()
		return "", fmt.Errorf("timeout waiting for %s to start", displayName)
	case <-ctx.Done():
		_ = execCmd.Process.Kill()
		return "", ctx.Err()
	}
}

type cscSession struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Directory string `json:"directory"`
	Version   int    `json:"version"`
	Status    string `json:"status"`
}

func (a *Agent) createSession(ctx context.Context) (*cscSession, error) {
	respBody, err := a.doPost(ctx, "/session", map[string]any{})
	if err != nil {
		return nil, err
	}
	var session cscSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return nil, fmt.Errorf("parse session response: %w", err)
	}
	return &session, nil
}

// CreateSession creates a csc session with the requested ID and working
// directory, waiting for a starting worker to become ready and replacing a
// stopped session. This lets workflow tasks expose a stable conversation URL
// that matches the multica chat_session.id.
func (a *Agent) CreateSession(ctx context.Context, sessionID, cwd string, env []string) error {
	return a.createSessionWithEnv(ctx, sessionID, cwd, env)
}

func (a *Agent) createSessionWithEnv(ctx context.Context, sessionID, cwd string, env []string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}

	// Avoid replacing an existing active session, but do not mistake a
	// persisted stopped-session stub for a ready worker.
	status, found, err := a.getSessionLifecycle(ctx, sessionID)
	if err == nil && found {
		switch status {
		case "", "running":
			return nil
		case "starting":
			return a.waitForSessionReady(ctx, sessionID)
		}
	}

	body := map[string]any{
		"session_id": sessionID,
		// "default" mode: read-only tools auto-allow, everything else asks.
		// The asks surface as permission.asked/question.asked SSE events so
		// the web UI can prompt the user while the task runs — bypassPermissions
		// would suppress them entirely and leave the session unsupervised.
		"permission_mode": "default",
	}
	if cwd != "" {
		body["cwd"] = cwd
	}
	if len(env) > 0 {
		body["env"] = envSliceToMap(env)
	}
	// csc registers POST /session without a trailing slash; Hono matches
	// strictly, so "/session/" would 404.
	response, err := a.doPost(ctx, "/session", body)
	if err != nil {
		return err
	}
	var created cscSession
	if err := json.Unmarshal(response, &created); err != nil {
		return fmt.Errorf("parse session response: %w", err)
	}
	if created.Status == "" {
		// Older csc servers did not expose lifecycle status. Preserve
		// compatibility rather than polling an endpoint that cannot prove ready.
		return nil
	}
	if created.Status == "running" {
		return nil
	}
	if created.Status == "stopping" || created.Status == "stopped" || created.Status == "detached" {
		return fmt.Errorf("session %s stopped before becoming ready (status=%s)", sessionID, created.Status)
	}
	return a.waitForSessionReady(ctx, sessionID)
}

func (a *Agent) getSessionLifecycle(ctx context.Context, sessionID string) (status string, found bool, err error) {
	resp, err := a.doRawGet(ctx, "/session/"+sessionID)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return "", false, fmt.Errorf("get session status: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var session cscSession
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return "", false, fmt.Errorf("parse session status: %w", err)
	}
	return session.Status, true, nil
}

func (a *Agent) waitForSessionReady(ctx context.Context, sessionID string) error {
	const pollInterval = 100 * time.Millisecond

	for {
		status, found, err := a.getSessionLifecycle(ctx, sessionID)
		if err != nil {
			return err
		}
		if found {
			switch status {
			case "", "running":
				return nil
			case "stopping", "stopped", "detached":
				return fmt.Errorf("session %s stopped before becoming ready (status=%s)", sessionID, status)
			}
		}

		timer := time.NewTimer(pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// PromptSession sends a prompt to an existing csc session asynchronously.
func (a *Agent) PromptSession(ctx context.Context, sessionID, content string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	body := map[string]any{"content": content}
	_, err := a.doPost(ctx, "/session/"+sessionID+"/prompt_async", body)
	return err
}

// sessionEvent is one parsed SSE event from the csc event stream.
type sessionEvent struct {
	name string
	data map[string]any
}

// subscribeSessionEvents opens the csc event stream filtered to the session
// and returns only after the HTTP connection is established. Callers can then
// send a prompt without missing the busy/idle events it produces.
func (a *Agent) subscribeSessionEvents(ctx context.Context, sessionID string) (<-chan sessionEvent, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.rawEndpoint+"/event?session_id="+sessionID, nil)
	if err != nil {
		return nil, err
	}

	// Use a dedicated client without a request timeout so the SSE stream can
	// stay open for the full workflow agent timeout.
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("event stream returned status %d", resp.StatusCode)
	}

	ch := make(chan sessionEvent, 64)
	go func() {
		defer resp.Body.Close()
		defer close(ch)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var event string
		var data map[string]any
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
				continue
			}
			if strings.HasPrefix(line, "data: ") {
				data = nil
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data)
				continue
			}
			if line == "" {
				if event != "" {
					select {
					case ch <- sessionEvent{name: event, data: data}:
					case <-ctx.Done():
						return
					}
				}
				event = ""
				data = nil
			}
		}
	}()
	return ch, nil
}

// waitForSessionDone consumes session events until the prompt finishes. It
// gates completion on having seen the session go busy first, so an idle event
// emitted before our prompt starts cannot end the wait early.
func waitForSessionDone(ctx context.Context, events <-chan sessionEvent) error {
	busy := false
	awaitingContinuation := false
	terminalFailure := false
	terminalSubtype := ""
	terminalMessage := ""
	finishIdle := func() (bool, error) {
		if !busy {
			return false, nil
		}
		if awaitingContinuation && !terminalFailure {
			// CSC emits an idle boundary after a model turn that ended in a
			// tool call (or token-limit continuation). The same prompt will
			// become busy again once tool execution/continuation resumes.
			busy = false
			return false, nil
		}
		return true, sessionCompletionError(terminalFailure, terminalSubtype, terminalMessage)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("event stream closed before the session finished")
			}
			switch ev.name {
			case "session.status":
				if status, ok := ev.data["status"].(map[string]any); ok {
					if t, _ := status["type"].(string); t == "busy" {
						if !busy {
							busy = true
							awaitingContinuation = false
							terminalFailure = false
							terminalSubtype = ""
							terminalMessage = ""
						}
					} else if t == "idle" && busy {
						if done, err := finishIdle(); done {
							return err
						}
					}
				}
			case "session.result":
				if !busy {
					continue
				}
				terminalSubtype, _ = ev.data["subtype"].(string)
				isError, _ := ev.data["isError"].(bool)
				if snakeCaseError, _ := ev.data["is_error"].(bool); snakeCaseError {
					isError = true
				}
				terminalFailure = isError || (terminalSubtype != "" && terminalSubtype != "success")
				stopReason, _ := ev.data["stopReason"].(string)
				if stopReason == "" {
					stopReason, _ = ev.data["stop_reason"].(string)
				}
				awaitingContinuation = stopReason == "tool_use" || stopReason == "max_tokens"
				if message := sessionResultErrorMessage(ev.data); message != "" {
					terminalMessage = message
				}
			case "session.error":
				if !busy {
					continue
				}
				errorData, _ := ev.data["error"].(map[string]any)
				if subtype, _ := errorData["subtype"].(string); subtype != "api_retry" {
					if message, _ := errorData["message"].(string); message != "" {
						terminalMessage = message
					}
				}
			case "session.idle":
				if done, err := finishIdle(); done {
					return err
				}
			}
		}
	}
}

func sessionResultErrorMessage(data map[string]any) string {
	if errorsList, ok := data["errors"].([]any); ok {
		for _, item := range errorsList {
			if message, ok := item.(string); ok && message != "" {
				return message
			}
			if errorData, ok := item.(map[string]any); ok {
				if message, _ := errorData["message"].(string); message != "" {
					return message
				}
			}
		}
	}
	if errorData, ok := data["error"].(map[string]any); ok {
		if message, _ := errorData["message"].(string); message != "" {
			return message
		}
	}
	if message, _ := data["error"].(string); message != "" {
		return message
	}
	if message, _ := data["message"].(string); message != "" {
		return message
	}
	return ""
}

func sessionCompletionError(failed bool, subtype, message string) error {
	if !failed {
		return nil
	}
	if message != "" {
		return fmt.Errorf("%s", message)
	}
	if subtype != "" {
		return fmt.Errorf("csc session failed: %s", subtype)
	}
	return fmt.Errorf("csc session failed")
}

// abortSession asks csc to abort the currently running prompt in a session.
func (a *Agent) abortSession(ctx context.Context, sessionID string) error {
	_, err := a.doPost(ctx, "/session/"+sessionID+"/abort", nil)
	return err
}

// AbortSession asks csc to stop a workflow prompt running in the given session.
func (a *Agent) AbortSession(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("session id is required")
	}
	return a.abortSession(ctx, sessionID)
}

// GetSessionMessages fetches the message list for a csc session.
func (a *Agent) GetSessionMessages(ctx context.Context, sessionID string) (json.RawMessage, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("session id is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.rawEndpoint+"/session/"+sessionID+"/message", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return json.RawMessage(body), nil
}

// RunSession creates (or ensures) a csc session, sends the prompt, waits for
// the session to finish, and returns the final assistant message text. The
// event subscription is established before the prompt is sent so the
// busy/idle events cannot race past the subscriber. If the context is
// cancelled (e.g. the task is aborted), the session prompt is aborted
// best-effort so the agent does not keep running detached.
func (a *Agent) RunSession(ctx context.Context, sessionID, cwd, prompt string, env []string) ([]byte, error) {
	if err := a.createSessionWithEnv(ctx, sessionID, cwd, env); err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}

	// Derive a cancelable context so the SSE subscription is closed when the
	// run ends instead of leaking one open connection per task.
	subCtx, stopSub := context.WithCancel(ctx)
	defer stopSub()

	events, err := a.subscribeSessionEvents(subCtx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("subscribe events: %w", err)
	}
	if err := a.PromptSession(ctx, sessionID, prompt); err != nil {
		return nil, fmt.Errorf("send prompt: %w", err)
	}
	if err := waitForSessionDone(subCtx, events); err != nil {
		abortCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = a.abortSession(abortCtx, sessionID)
		cancel()
		return nil, fmt.Errorf("wait for completion: %w", err)
	}
	body, err := a.GetSessionMessages(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get messages: %w", err)
	}
	text, err := extractLastAssistantText(body)
	if err != nil {
		return nil, fmt.Errorf("extract output: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%w: %s", agent.ErrEmptySessionOutput, sessionID)
	}
	return []byte(text), nil
}

func envSliceToMap(env []string) map[string]string {
	result := make(map[string]string, len(env))
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key == "" {
			continue
		}
		result[key] = value
	}
	return result
}

func extractLastAssistantText(body json.RawMessage) (string, error) {
	var envelope struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("parse messages: %w", err)
	}

	for i := len(envelope.Messages) - 1; i >= 0; i-- {
		msg := envelope.Messages[i]
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}

		for _, candidate := range []any{msg["parts"], msg["content"]} {
			if text := extractTextContent(candidate); strings.TrimSpace(text) != "" {
				return text, nil
			}
		}
	}
	return "", nil
}

func extractTextContent(content any) string {
	if wrapper, ok := content.(map[string]any); ok {
		content = wrapper["content"]
	}
	parts, _ := content.([]any)
	var b strings.Builder
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := part["type"].(string); t != "text" {
			continue
		}
		if text, ok := part["text"].(string); ok && text != "" {
			if b.Len() > 0 {
				b.WriteByte('\n')
			}
			b.WriteString(text)
		}
	}
	return b.String()
}

func (a *Agent) subscribeEvents(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.rawEndpoint+"/event", nil)
	if err != nil {
		logger.Error("csc event subscribe: %v", err)
		return
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			logger.Error("csc event stream error: %v", err)
		}
		return
	}
	defer resp.Body.Close()

	logger.Info("[csc-events] subscribed to %s/event", a.rawEndpoint)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var sseEvent string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := scanner.Text()
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "event: ") {
			sseEvent = strings.TrimPrefix(trimmed, "event: ")
			continue
		}

		if !strings.HasPrefix(trimmed, "data: ") {
			if trimmed == "" {
				sseEvent = ""
			}
			continue
		}
		data := strings.TrimPrefix(trimmed, "data: ")

		var raw map[string]any
		if err := json.Unmarshal([]byte(data), &raw); err != nil {
			logger.Debug("[csc-events] JSON parse error: %v data=%.80s", err, data)
			sseEvent = ""
			continue
		}

		eventType, _ := raw["type"].(string)
		if eventType == "" && sseEvent != "" && sseEvent != "message" {
			eventType = sseEvent
		}
		sseEvent = ""

		props, _ := raw["properties"].(map[string]any)
		if props == nil {
			props = raw
		}

		if eventType == "" {
			continue
		}

		if eventType == "permission.asked" || eventType == "question.asked" ||
			eventType == "permission.responded" || eventType == "question.responded" ||
			eventType == "session.idle" {
			logger.Info("[csc-events] emitting: type=%s sessionID=%s", eventType, a.sessionID)
		}

		a.emit(agent.Event{
			Type:           eventType,
			ConversationID: a.sessionID,
			Backend:        "csc",
			Data:           props,
		})
	}

	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return
		}
		logger.Warn("[csc-events] scanner error: %v, reconnecting", err)
	} else {
		logger.Info("[csc-events] stream ended, reconnecting")
	}
	time.Sleep(time.Second)
	go a.subscribeEvents(ctx)
}

func (a *Agent) doGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.endpoint+path, nil)
	if err != nil {
		return nil, err
	}
	return a.httpClient.Do(req)
}

func (a *Agent) doRawGet(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.rawEndpoint+path, nil)
	if err != nil {
		return nil, err
	}
	return a.httpClient.Do(req)
}

func (a *Agent) doPost(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return a.doPostBase(ctx, a.endpoint, path, body)
}

func (a *Agent) doRawPost(ctx context.Context, path string, body any) (json.RawMessage, error) {
	return a.doPostBase(ctx, a.rawEndpoint, path, body)
}

func (a *Agent) doPostBase(ctx context.Context, base string, path string, body any) (json.RawMessage, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	return json.RawMessage(respBody), nil
}
