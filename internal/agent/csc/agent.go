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

	command    agent.Command
	workDir    string
	customEnv  map[string]string
	endpoint   string
	rawEndpoint string
	cmd        *exec.Cmd
	waitCh     chan error
	cancel     context.CancelFunc
	adapter    *AdapterServer

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
		id:         cfg.ID,
		command:    cmd,
		workDir:    cfg.WorkingDir,
		customEnv:  cfg.CustomEnv,
		state:      agent.StateIdle,
		httpClient: &http.Client{
			Timeout: 300 * time.Second,
		},
	}
}

func (a *Agent) ID() string     { return a.id }
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
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint+"/health", nil)
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
}

func (a *Agent) createSession(ctx context.Context) (*cscSession, error) {
	respBody, err := a.doPost(ctx, "/session/", map[string]any{})
	if err != nil {
		return nil, err
	}
	var session cscSession
	if err := json.Unmarshal(respBody, &session); err != nil {
		return nil, fmt.Errorf("parse session response: %w", err)
	}
	return &session, nil
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
