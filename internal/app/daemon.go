package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (a *App) pidFile() string {
	return filepath.Join(a.rootDir, "cs-cloud.pid")
}

func (a *App) agentPidFile() string {
	return filepath.Join(a.rootDir, "agent.pid")
}

func (a *App) logFile() string {
	return filepath.Join(a.rootDir, "app.log")
}

func (a *App) stopFile() string {
	return filepath.Join(a.rootDir, "cloud.stop")
}

func (a *App) stateFile() string {
	return filepath.Join(a.rootDir, "state")
}

func (a *App) serverFile() string {
	return filepath.Join(a.rootDir, "server_url")
}

func (a *App) modeFile() string {
	return filepath.Join(a.rootDir, "mode")
}

func (a *App) argsFile() string {
	return filepath.Join(a.rootDir, "daemon_args")
}

func (a *App) SaveArgs(args []string) error {
	if err := a.EnsureRootDir(); err != nil {
		return err
	}
	data := strings.Join(args, "\n")
	return os.WriteFile(a.argsFile(), []byte(data), 0o644)
}

func (a *App) LoadArgs() []string {
	data, err := os.ReadFile(a.argsFile())
	if err != nil {
		return nil
	}
	var args []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			args = append(args, line)
		}
	}
	return args
}

func (a *App) ReadPID() (int, error) {
	b, err := os.ReadFile(a.pidFile())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func (a *App) WritePID(pid int) error {
	if err := a.EnsureRootDir(); err != nil {
		return err
	}
	return os.WriteFile(a.pidFile(), []byte(strconv.Itoa(pid)), 0o600)
}

func (a *App) RemovePID() {
	os.Remove(a.pidFile())
}

func (a *App) WriteAgentPID(pid int) error {
	if err := a.EnsureRootDir(); err != nil {
		return err
	}
	return os.WriteFile(a.agentPidFile(), []byte(strconv.Itoa(pid)), 0o600)
}

func (a *App) ReadAgentPID() (int, error) {
	b, err := os.ReadFile(a.agentPidFile())
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

func (a *App) RemoveAgentPID() {
	os.Remove(a.agentPidFile())
}

func (a *App) DaemonStatus() (bool, int, string) {
	pid, err := a.ReadPID()
	if err != nil {
		return false, 0, "pid file not found"
	}
	if !a.IsProcessRunning(pid) {
		return false, pid, fmt.Sprintf("process %d not running", pid)
	}
	running, state, _ := a.IsRunning()
	if !running {
		return false, pid, fmt.Sprintf("unexpected state %q", state)
	}
	if !a.healthCheck() {
		return false, pid, "health check failed"
	}
	return true, pid, ""
}

func (a *App) healthCheck() bool {
	serverURL, err := a.ServerURL()
	if err != nil || serverURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, serverURL+"/api/v1/runtime/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (a *App) ForceCleanupStale() bool {
	cleaned := false
	if pid, err := a.ReadPID(); err == nil && pid > 0 && a.IsProcessRunning(pid) {
		a.forceKillStale(pid)
		cleaned = true
	}
	if agentPID, err := a.ReadAgentPID(); err == nil && agentPID > 0 && a.IsProcessRunning(agentPID) {
		a.forceKillStale(agentPID)
		cleaned = true
	}
	if killOrphanProcesses(a.rootDir) {
		cleaned = true
	}
	a.cleanupAllStateFiles()
	return cleaned
}

func (a *App) cleanupAllStateFiles() {
	a.RemovePID()
	a.RemoveAgentPID()
	a.RemoveStopFile()
	a.SaveState("stopped")
	a.SaveServerURL("")
}

func (a *App) ClearDaemonReadiness() {
	a.RemovePID()
	a.RemoveAgentPID()
	_ = os.Remove(a.stateFile())
	_ = a.SaveServerURL("")
}

func (a *App) forceKillStale(pid int) {
	if !a.IsProcessRunning(pid) {
		return
	}
	forceKillProcess(pid)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !a.IsProcessRunning(pid) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	forceKillProcess(pid)
}

func (a *App) SaveMode(mode string) error {
	if err := a.EnsureRootDir(); err != nil {
		return err
	}
	return os.WriteFile(a.modeFile(), []byte(mode), 0o644)
}

func (a *App) LoadMode() string {
	b, err := os.ReadFile(a.modeFile())
	if err != nil {
		return "cloud"
	}
	return strings.TrimSpace(string(b))
}

func (a *App) RemoveStopFile() {
	os.Remove(a.stopFile())
}

func (a *App) StopFileExists() bool {
	_, err := os.Stat(a.stopFile())
	return err == nil
}

func (a *App) OpenLogFile() (*os.File, error) {
	if err := a.EnsureRootDir(); err != nil {
		return nil, err
	}
	return os.OpenFile(a.logFile(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
}

func (a *App) LogFilePath() string {
	return a.logFile()
}

func (a *App) IsRunning() (bool, string, error) {
	if err := a.EnsureRootDir(); err != nil {
		return false, "", err
	}
	b, err := os.ReadFile(a.stateFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, "", nil
		}
		return false, "", err
	}
	state := strings.TrimSpace(string(b))
	return state == "running", state, nil
}

func (a *App) SaveState(state string) error {
	if err := a.EnsureRootDir(); err != nil {
		return err
	}
	return os.WriteFile(a.stateFile(), []byte(state+"\n"), 0o644)
}

func (a *App) ServerURL() (string, error) {
	if err := a.EnsureRootDir(); err != nil {
		return "", err
	}
	b, err := os.ReadFile(a.serverFile())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func (a *App) SaveServerURL(raw string) error {
	if err := a.EnsureRootDir(); err != nil {
		return err
	}
	if raw == "" {
		os.Remove(a.serverFile())
		return nil
	}
	return os.WriteFile(a.serverFile(), []byte(raw+"\n"), 0o644)
}

func dataDirFromArgs(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--data-dir" && i+1 < len(args):
			return args[i+1]
		case len(args[i]) > 11 && args[i][:11] == "--data-dir=":
			return args[i][11:]
		}
	}
	return ""
}

func rootDirFromDataDir(dataDir string) string {
	if dataDir != "" {
		if !filepath.IsAbs(dataDir) {
			if abs, err := filepath.Abs(dataDir); err == nil {
				dataDir = abs
			}
		}
		return filepath.Join(dataDir, "cs-cloud")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".costrict", "cs-cloud")
}
