package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"cs-cloud/internal/agent"
	agentcs "cs-cloud/internal/agent/cs"
	agentcsc "cs-cloud/internal/agent/csc"
)

// ProgressFunc is a callback for reporting operation progress.
type ProgressFunc func(phase string, progress float64, message string)

type AgentManager struct {
	mu       sync.RWMutex
	agents   map[string]agent.Agent
	drivers  map[string]agent.Driver
	eventBus *EventBus
}

func NewAgentManager(eventBus *EventBus) *AgentManager {
	return &AgentManager{
		agents:   make(map[string]agent.Agent),
		drivers:  make(map[string]agent.Driver),
		eventBus: eventBus,
	}
}

func (m *AgentManager) RegisterDriver(d agent.Driver) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drivers[d.Name()] = d
}

func (m *AgentManager) GetDriver(name string) (agent.Driver, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	d, ok := m.drivers[name]
	return d, ok
}

func (m *AgentManager) ResolveDriver(backend string) (agent.Driver, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	d, ok := m.drivers[backend]
	if ok {
		return d, nil
	}

	return nil, fmt.Errorf("no driver for backend: %s", backend)
}

func (m *AgentManager) CreateAgent(ctx context.Context, convID string, cfg agent.AgentConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.agents[convID]; exists {
		return fmt.Errorf("agent already exists for conversation: %s", convID)
	}

	d, ok := m.drivers[cfg.Backend]
	if !ok {
		return fmt.Errorf("no driver for backend: %s", cfg.Backend)
	}

	a, err := d.CreateAgent(cfg)
	if err != nil {
		return err
	}

	a.SetEventEmitter(func(event agent.Event) {
		event.ConversationID = convID
		if m.eventBus != nil {
			m.eventBus.Emit(event)
		}
	})

	if err := a.Start(ctx); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	m.agents[convID] = a
	return nil
}

func (m *AgentManager) GetAgent(convID string) (agent.Agent, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	a, ok := m.agents[convID]
	return a, ok
}

func (m *AgentManager) ListAgents() []agent.Agent {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]agent.Agent, 0, len(m.agents))
	for _, a := range m.agents {
		result = append(result, a)
	}
	return result
}

func (m *AgentManager) SendMessage(ctx context.Context, convID string, msg agent.PromptMessage) error {
	a, ok := m.GetAgent(convID)
	if !ok {
		return fmt.Errorf("agent not found: %s", convID)
	}
	return a.SendMessage(ctx, msg)
}

func (m *AgentManager) CancelPrompt(ctx context.Context, convID string) error {
	a, ok := m.GetAgent(convID)
	if !ok {
		return fmt.Errorf("agent not found: %s", convID)
	}
	return a.CancelPrompt(ctx)
}

func (m *AgentManager) ConfirmPermission(ctx context.Context, convID string, callID string, optionID string) error {
	a, ok := m.GetAgent(convID)
	if !ok {
		return fmt.Errorf("agent not found: %s", convID)
	}
	return a.ConfirmPermission(ctx, callID, optionID)
}

func (m *AgentManager) KillAgent(convID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if a, ok := m.agents[convID]; ok {
		_ = a.Kill()
		delete(m.agents, convID)
	}
}

func (m *AgentManager) KillAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, a := range m.agents {
		_ = a.Kill()
		delete(m.agents, id)
	}
}

func (m *AgentManager) AgentPID() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.agents {
		if pid := a.PID(); pid > 0 {
			return pid
		}
	}
	return 0
}

func (m *AgentManager) DetectAgents(ctx context.Context) ([]agent.DetectedAgent, error) {
	m.mu.RLock()
	drivers := make([]agent.Driver, 0, len(m.drivers))
	for _, d := range m.drivers {
		drivers = append(drivers, d)
	}
	m.mu.RUnlock()

	var all []agent.DetectedAgent
	for _, d := range drivers {
		agents, err := d.Detect(ctx)
		if err != nil {
			continue
		}
		all = append(all, agents...)
	}
	return all, nil
}

func (m *AgentManager) Endpoint() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.agents {
		if e, ok := a.(interface{ Endpoint() string }); ok {
			if ep := e.Endpoint(); ep != "" {
				return ep
			}
		}
	}
	return ""
}

func (m *AgentManager) DefaultBackend() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.agents {
		return a.Backend()
	}
	return ""
}

func (m *AgentManager) PrewarmPaths() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.agents {
		if d, ok := m.drivers[a.Backend()]; ok {
			return d.PrewarmPaths()
		}
	}
	return nil
}

func (m *AgentManager) WorkspaceHeaderName() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.agents {
		if d, ok := m.drivers[a.Backend()]; ok {
			for _, v := range d.HeaderMap() {
				return v
			}
		}
	}
	return ""
}

func (m *AgentManager) InitDefaultAgent(ctx context.Context, agentType string, agentCommand string, agentVersionCommand string, agentWorkspace string, agentEnv map[string]string) error {
	if agentType == "" {
		agentType = "cs"
	}

	var defaultCmd string
	switch agentType {
	case "cs":
		defaultCmd = agentcs.CLIBinary + " serve"
	case "csc":
		defaultCmd = agentcsc.CLIBinary + " serve"
	default:
		return fmt.Errorf("unknown agent type: %s (available: cs, csc)", agentType)
	}

	cmd := agent.ParseCommand(defaultCmd)
	if agentCommand != "" {
		cmd = agent.ParseCommand(agentCommand)
	}

	csDriver := agentcs.NewDriver(cmd)
	cscDriver := agentcsc.NewDriver(cmd)
	if agentVersionCommand != "" {
		csDriver.SetVersionCommand(agentVersionCommand)
		cscDriver.SetVersionCommand(agentVersionCommand)
	}

	drivers := map[string]agent.Driver{
		"cs":  csDriver,
		"csc": cscDriver,
	}

	for name, d := range drivers {
		m.RegisterDriver(d)
		_ = name
	}

	d, ok := drivers[agentType]
	if !ok {
		return fmt.Errorf("unknown agent type: %s (available: cs, csc)", agentType)
	}

	detected, _ := d.Detect(ctx)
	if len(detected) == 0 || !detected[0].Available {
		return fmt.Errorf("agent command '%s' not found, please ensure it is installed correctly", strings.Join(cmd.Args, " "))
	}

	var extra map[string]any
	if v, ok := detected[0].Extra.(map[string]any); ok {
		extra = v
	}
	cfg := agent.AgentConfig{
		ID:         "default",
		Backend:    agentType,
		DriverName: "http",
		WorkingDir: agentWorkspace,
		CustomEnv:  agentEnv,
		Extra:      extra,
	}
	return m.CreateAgent(ctx, "default", cfg)
}

// RestartDefaultAgent kills all running agents and re-initializes the default agent.
// The onProgress callback receives phased progress updates during the operation.
func (m *AgentManager) RestartDefaultAgent(ctx context.Context, agentType, agentCommand, agentVersionCommand, agentWorkspace string, agentEnv map[string]string, onProgress ProgressFunc) error {
	if onProgress == nil {
		onProgress = func(phase string, progress float64, message string) {}
	}

	onProgress("killing", 0.1, "Stopping running agents")
	m.KillAll()
	onProgress("killed", 0.3, "All agents stopped")

	onProgress("detecting", 0.4, "Detecting agent binary")
	if err := m.InitDefaultAgent(ctx, agentType, agentCommand, agentVersionCommand, agentWorkspace, agentEnv); err != nil {
		onProgress("failed", 0.0, err.Error())
		return err
	}

	// Emit restart event after agent is fully ready so all active SSE connections
	// receive it and clients can safely rebootstrap.
	if m.eventBus != nil {
		m.eventBus.Emit(agent.Event{
			Type: "agent.runtime.restarted",
			Data: map[string]any{"backend": agentType},
		})
	}

	onProgress("ready", 1.0, "Agent restart complete")
	return nil
}

func (m *AgentManager) GetAgentVersion(backend string) (string, error) {
	d, err := m.ResolveDriver(backend)
	if err != nil {
		return "", err
	}
	return d.Version()
}

func (m *AgentManager) HealthCheck(ctx context.Context, backend string) (*agent.HealthResult, error) {
	d, err := m.ResolveDriver(backend)
	if err != nil {
		return nil, err
	}
	return d.HealthCheck(ctx, backend)
}
