package localserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"cs-cloud/internal/app"
	"cs-cloud/internal/device"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/updater"
)

type reconnecter interface {
	Reconnect()
}

type agentRestarter func(ctx context.Context, onProgress func(phase string, progress float64, message string)) error

type commandEntry struct {
	req         *commandRequest
	status      string
	startedAt   time.Time
	completedAt time.Time
	result      any
	errMsg      string
	phase       string
	progress    float64
	message     string
}

type persistedEntry struct {
	CommandID   string          `json:"command_id"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Timestamp   string          `json:"timestamp,omitempty"`
	Status      string          `json:"status"`
	StartedAt   time.Time       `json:"started_at"`
	CompletedAt time.Time       `json:"completed_at"`
	Result      json.RawMessage `json:"result,omitempty"`
	ErrMsg      string          `json:"err_msg,omitempty"`
	Phase       string          `json:"phase,omitempty"`
	Progress    float64         `json:"progress,omitempty"`
	Message     string          `json:"message,omitempty"`
}

func (e *commandEntry) toPersisted() *persistedEntry {
	pe := &persistedEntry{
		CommandID:   e.req.CommandID,
		Type:        e.req.Type,
		Payload:     e.req.Payload,
		Timestamp:   e.req.Timestamp,
		Status:      e.status,
		StartedAt:   e.startedAt,
		CompletedAt: e.completedAt,
		ErrMsg:      e.errMsg,
		Phase:       e.phase,
		Progress:    e.progress,
		Message:     e.message,
	}
	if e.result != nil {
		pe.Result, _ = json.Marshal(e.result)
	}
	return pe
}

func (pe *persistedEntry) toEntry() *commandEntry {
	return &commandEntry{
		req: &commandRequest{
			CommandID: pe.CommandID,
			Type:      pe.Type,
			Payload:   pe.Payload,
			Timestamp: pe.Timestamp,
		},
		status:      pe.Status,
		startedAt:   pe.StartedAt,
		completedAt: pe.CompletedAt,
		errMsg:      pe.ErrMsg,
		phase:       pe.Phase,
		progress:    pe.Progress,
		message:     pe.Message,
	}
}

const commandStateFile = "command_status.json"

type CommandDispatcher struct {
	mu            sync.Mutex
	active        map[string]*commandEntry
	updater       *updater.Manager
	tunnel        reconnecter
	restarter     func()
	agentRestarter agentRestarter
	reporter      *CommandReporter
	app           *app.App
	startedAt     time.Time
	upgradeGrace  bool
}

func NewCommandDispatcher(a *app.App, reporter *CommandReporter) *CommandDispatcher {
	d := &CommandDispatcher{
		active:    make(map[string]*commandEntry),
		app:       a,
		reporter:  reporter,
		startedAt: time.Now(),
	}
	d.restoreCommands()
	return d
}

func (d *CommandDispatcher) BindUpdater(u *updater.Manager) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.updater = u
}

func (d *CommandDispatcher) BindTunnel(m reconnecter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.tunnel = m
}

func (d *CommandDispatcher) BindRestarter(fn func()) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.restarter = fn
}

func (d *CommandDispatcher) BindAgentRestarter(fn agentRestarter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agentRestarter = fn
}

func (d *CommandDispatcher) BindDeviceClient(c *device.Client) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.reporter != nil {
		d.reporter.deviceClient = c
	}
}

func (d *CommandDispatcher) MarkUpgradeVerified() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.upgradeGrace = true
}

const upgradeGracePeriod = 30 * time.Second

func (d *CommandDispatcher) Dispatch(ctx context.Context, req *commandRequest) (*commandAck, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.active[req.CommandID]; exists {
		return nil, fmt.Errorf("command %s already exists", req.CommandID)
	}

	for _, entry := range d.active {
		if entry.status == "executing" || entry.status == "accepted" {
			return nil, fmt.Errorf("command %s is already %s", entry.req.CommandID, entry.status)
		}
	}

	if d.upgradeGrace && (req.Type == "restart" || req.Type == "upgrade") {
		elapsed := time.Since(d.startedAt)
		if elapsed < upgradeGracePeriod {
			logger.Warn("[dispatcher] rejecting stale %s command %s during upgrade grace period (elapsed=%s)", req.Type, req.CommandID, elapsed.Round(time.Millisecond))
			return nil, fmt.Errorf("rejecting %s command during upgrade grace period", req.Type)
		}
		d.upgradeGrace = false
	}

	entry := &commandEntry{
		req:       req,
		status:    "accepted",
		startedAt: time.Now(),
	}
	d.active[req.CommandID] = entry

	go d.execute(req)

	return writeCommandAck(req.CommandID, "accepted", fmt.Sprintf("%s scheduled", req.Type)), nil
}

func (d *CommandDispatcher) Status(commandID string) (*commandStatusResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, exists := d.active[commandID]
	if !exists {
		return nil, fmt.Errorf("command %s not found", commandID)
	}

	return writeCommandStatusWithProgress(entry.req, entry.status, entry.errMsg, entry.startedAt, entry.completedAt, entry.result, entry.phase, entry.progress, entry.message), nil
}

func (d *CommandDispatcher) UpdateProgress(commandID, phase string, progress float64, message string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	entry, exists := d.active[commandID]
	if !exists {
		return
	}
	entry.phase = phase
	entry.progress = progress
	entry.message = message
}

func (d *CommandDispatcher) commandStatePath() string {
	if d.app == nil {
		return ""
	}
	return filepath.Join(d.app.RootDir(), commandStateFile)
}

func (d *CommandDispatcher) persistCommands() {
	path := d.commandStatePath()
	if path == "" {
		return
	}

	d.mu.Lock()
	var entries []*persistedEntry
	for _, entry := range d.active {
		if entry.status == "completed" || entry.status == "failed" {
			entries = append(entries, entry.toPersisted())
		}
	}
	d.mu.Unlock()

	if len(entries) == 0 {
		os.Remove(path)
		return
	}

	data, err := json.Marshal(entries)
	if err != nil {
		logger.Warn("[dispatcher] failed to marshal command state: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		logger.Warn("[dispatcher] failed to persist command state: %v", err)
	}
}

func (d *CommandDispatcher) restoreCommands() {
	path := d.commandStatePath()
	if path == "" {
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	var entries []*persistedEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		logger.Warn("[dispatcher] failed to unmarshal command state: %v", err)
		return
	}

	now := time.Now()
	for _, pe := range entries {
		if now.Sub(pe.CompletedAt) > 10*time.Minute {
			continue
		}
		d.active[pe.CommandID] = pe.toEntry()
	}

	os.Remove(path)
}

func (d *CommandDispatcher) HandleHeartbeatCommands(cmds []device.CloudCommand) {
	for _, cmd := range cmds {
		req := &commandRequest{
			CommandID: cmd.CommandID,
			Type:      cmd.Type,
			Payload:   cmd.Payload,
			Timestamp: cmd.Timestamp,
		}
		if _, err := d.Dispatch(context.Background(), req); err != nil {
			logger.Warn("[dispatcher] heartbeat command %s rejected: %v", cmd.CommandID, err)
		}
	}
}

func (d *CommandDispatcher) execute(req *commandRequest) {
	d.mu.Lock()
	entry := d.active[req.CommandID]
	entry.status = "executing"
	d.mu.Unlock()

	logger.Info("[dispatcher] executing command %s (type=%s)", req.CommandID, req.Type)

	var result any
	var err error

	switch req.Type {
	case "upgrade":
		result, err = d.execUpgrade(req)
	case "restart":
		result, err = d.execRestart(req)
	case "restart-agent":
		result, err = d.execRestartAgent(req)
	case "reconnect":
		result, err = d.execReconnect(req)
	default:
		err = fmt.Errorf("unknown command type: %s", req.Type)
	}

	d.mu.Lock()
	if err != nil {
		entry.status = "failed"
		entry.errMsg = err.Error()
		logger.Error("[dispatcher] command %s failed: %v", req.CommandID, err)
	} else {
		entry.status = "completed"
		entry.result = result
		logger.Info("[dispatcher] command %s completed", req.CommandID)
	}
	entry.completedAt = time.Now()
	needPersist := req.Type == "upgrade" && entry.status == "completed"
	d.mu.Unlock()

	if needPersist {
		d.persistCommands()
	}

	go d.reportResult(req.CommandID, entry)
}

func (d *CommandDispatcher) execUpgrade(req *commandRequest) (any, error) {
	d.mu.Lock()
	u := d.updater
	d.mu.Unlock()
	if u == nil {
		return nil, fmt.Errorf("updater not available")
	}

	var payload struct {
		Version     string `json:"version"`
		Force       bool   `json:"force"`
		DownloadURL string `json:"download_url"`
		SHA256      string `json:"sha256"`
	}
	if len(req.Payload) > 0 {
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid payload: %w", err)
		}
	}

	if payload.DownloadURL != "" {
		result, err := u.CheckNow(context.Background())
		if err != nil {
			return nil, err
		}
		if result.DownloadURL == "" {
			return nil, fmt.Errorf("no update available")
		}
	}

	commandID := req.CommandID
	onProgress := func(phase string, progress float64, message string) {
		d.UpdateProgress(commandID, phase, progress, message)
	}

	if err := u.ApplyWithProgress(context.Background(), payload.Version, onProgress); err != nil {
		return nil, err
	}

	return map[string]string{"status": "upgrade completed, restart pending"}, nil
}

func (d *CommandDispatcher) execRestart(_ *commandRequest) (any, error) {
	d.mu.Lock()
	fn := d.restarter
	d.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("restarter not available")
	}

	go func() {
		time.Sleep(1 * time.Second)
		fn()
	}()

	return map[string]string{"status": "restart scheduled"}, nil
}

func (d *CommandDispatcher) execRestartAgent(req *commandRequest) (any, error) {
	d.mu.Lock()
	fn := d.agentRestarter
	d.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("agent restarter not available")
	}

	commandID := req.CommandID
	onProgress := func(phase string, progress float64, message string) {
		d.UpdateProgress(commandID, phase, progress, message)
	}

	ctx := context.Background()
	if err := fn(ctx, onProgress); err != nil {
		return nil, err
	}

	return map[string]string{"status": "agent restarted"}, nil
}

func (d *CommandDispatcher) execReconnect(_ *commandRequest) (any, error) {
	d.mu.Lock()
	m := d.tunnel
	d.mu.Unlock()
	if m == nil {
		return nil, fmt.Errorf("tunnel manager not available")
	}

	m.Reconnect()
	return map[string]string{"status": "reconnect initiated"}, nil
}

func (d *CommandDispatcher) reportResult(commandID string, entry *commandEntry) {
	if d.reporter == nil {
		return
	}

	result := &commandStatusResponse{
		CommandID:   commandID,
		Type:        entry.req.Type,
		Status:      entry.status,
		Result:      entry.result,
	}
	if !entry.startedAt.IsZero() {
		result.StartedAt = entry.startedAt.Format(time.RFC3339)
	}
	if !entry.completedAt.IsZero() {
		result.CompletedAt = entry.completedAt.Format(time.RFC3339)
	}
	if entry.errMsg != "" {
		result.Error = entry.errMsg
	}

	d.reporter.Report(commandID, result)
}

func (d *CommandDispatcher) Cleanup(maxAge time.Duration) {
	d.mu.Lock()
	now := time.Now()
	for id, entry := range d.active {
		if entry.status != "executing" && now.Sub(entry.completedAt) > maxAge {
			delete(d.active, id)
		}
	}
	d.mu.Unlock()

	d.persistCommands()
}
