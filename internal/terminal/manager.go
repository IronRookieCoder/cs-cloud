package terminal

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"cs-cloud/internal/config"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/platform"
)

const defaultMaxSlots = 20

type TerminalManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	shell    string
	fallbackShells []string
	maxSlots int
	cfg      *config.Config
}

type Option func(*TerminalManager)

func WithMaxSlots(n int) Option {
	return func(m *TerminalManager) { m.maxSlots = n }
}

func WithConfig(cfg *config.Config) Option {
	return func(m *TerminalManager) { m.cfg = cfg }
}

func NewManager(opts ...Option) *TerminalManager {
	m := &TerminalManager{
		sessions: make(map[string]*Session),
		maxSlots: defaultMaxSlots,
	}
	for _, o := range opts {
		o(m)
	}
	m.shell, m.fallbackShells = discoverShell(m.cfg)
	logger.Info("terminal: shell=%s, fallbacks=%v, maxSlots=%d", m.shell, m.fallbackShells, m.maxSlots)
	return m
}

func (m *TerminalManager) Create(cwd string, rows, cols uint16) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.sessions) >= m.maxSlots {
		return nil, fmt.Errorf("terminal: max sessions (%d) reached: %w", m.maxSlots, ErrSessionLimit)
	}

	id := generateID()
	s, shell, err := m.newSessionWithFallback(id, cwd, rows, cols)
	if err != nil {
		return nil, &SessionCreateError{Err: err}
	}

	m.sessions[id] = s
	if shell != m.shell {
		logger.Warn("terminal: falling back from %s to %s", m.shell, shell)
		m.shell = shell
	}
	logger.Info("terminal: session created id=%s pid=%d", id, s.Pid)
	return s, nil
}

func (m *TerminalManager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("terminal: session %s not found", id)
	}
	return s, nil
}

func (m *TerminalManager) Kill(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("terminal: session %s not found", id)
	}

	s.Close()
	delete(m.sessions, id)
	logger.Info("terminal: session killed id=%s", id)
	return nil
}

func (m *TerminalManager) Resize(id string, rows, cols uint16) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	return s.Resize(rows, cols)
}

func (m *TerminalManager) Restart(id string, cwd string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	old, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("terminal: session %s not found", id)
	}

	if cwd == "" {
		cwd = old.Cwd
	}
	rows, cols := old.Rows, old.Cols

	old.Close()

	newS, shell, err := m.newSessionWithFallback(id, cwd, rows, cols)
	if err != nil {
		delete(m.sessions, id)
		return nil, fmt.Errorf("terminal: restart session: %w", err)
	}

	m.sessions[id] = newS
	if shell != m.shell {
		logger.Warn("terminal: falling back from %s to %s", m.shell, shell)
		m.shell = shell
	}
	logger.Info("terminal: session restarted id=%s pid=%d", id, newS.Pid)
	return newS, nil
}

func (m *TerminalManager) Write(id string, data []byte) error {
	s, err := m.Get(id)
	if err != nil {
		return err
	}
	return s.Write(data)
}

func (m *TerminalManager) Subscribe(id string) (<-chan []byte, func(), error) {
	s, err := m.Get(id)
	if err != nil {
		return nil, nil, err
	}
	ch, unsub := s.Subscribe()
	return ch, unsub, nil
}

func (m *TerminalManager) CleanupIdle(timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, s := range m.sessions {
		if now.Sub(s.LastInput) > timeout {
			s.Close()
			delete(m.sessions, id)
			logger.Info("terminal: idle session cleaned up id=%s", id)
		}
	}
}

func (m *TerminalManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		s.Close()
		delete(m.sessions, id)
	}
	logger.Info("terminal: all sessions closed")
}

func (m *TerminalManager) SessionCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}

func newSession(id string, shell string, cwd string, rows, cols uint16) (*Session, error) {
	ptmx, pid, err := startPty(shell, cwd, rows, cols)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:          id,
		Pid:         pid,
		Cols:        cols,
		Rows:        rows,
		Cwd:         cwd,
		CreatedAt:    time.Now(),
		LastInput:   time.Now(),
		cancel:      cancel,
		subscribers: make(map[string]chan []byte),
		ptmx:        ptmx,
		recentBuf:   make([][]byte, 0),
		recentMax:   1000,
	}

	go s.readOutput(ctx)
	return s, nil
}

func (m *TerminalManager) newSessionWithFallback(id string, cwd string, rows, cols uint16) (*Session, string, error) {
	candidates := make([]string, 0, 1+len(m.fallbackShells))
	if m.shell != "" {
		candidates = append(candidates, m.shell)
	}
	candidates = append(candidates, m.fallbackShells...)
	logger.Info("terminal: create session candidates=%v cwd=%q rows=%d cols=%d", candidates, cwd, rows, cols)

	var lastErr error
	for i, shell := range candidates {
		logger.Info("terminal: trying shell=%s", shell)
		s, err := newSession(id, shell, cwd, rows, cols)
		if err == nil {
			if i > 0 {
				m.fallbackShells = reorderFallbacks(candidates, shell)
			}
			logger.Info("terminal: shell=%s succeeded", shell)
			return s, shell, nil
		}
		lastErr = err
		logger.Warn("terminal: shell=%s failed: %v", shell, err)
		if !IsPermissionError(err) {
			return nil, shell, err
		}
		logger.Warn("terminal: shell %s failed with permission error, trying fallback", shell)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no terminal shell available")
	}
	logger.Error("terminal: all shell candidates failed: %v", lastErr)
	return nil, "", lastErr
}

func discoverShell(cfg *config.Config) (string, []string) {
	if runtime.GOOS == "windows" {
		return discoverWindowsShell(), nil
	}
	return discoverUnixShells(cfg)
}

func discoverUnixShells(cfg *config.Config) (string, []string) {
	candidates := []string{}
	if cfg != nil && cfg.DefaultShell != "" {
		candidates = append(candidates, cfg.DefaultShell)
	}
	if sh := platform.GetenvCompat("CS_BRIDGE_SHELL", "CS_CLOUD_SHELL"); sh != "" {
		candidates = append(candidates, sh)
	}
	candidates = append(candidates, os.Getenv("SHELL"), "/bin/zsh", "/bin/bash", "/bin/sh")

	resolved := make([]string, 0, len(candidates))
	for _, c := range candidates {
		p := resolveShellPath(c)
		if p == "" {
			continue
		}
		if containsString(resolved, p) {
			continue
		}
		resolved = append(resolved, p)
	}

	validated := make([]string, 0, len(resolved))
	for _, p := range resolved {
		if verifyShellPty(p) {
			validated = append(validated, p)
			continue
		}
		logger.Warn("terminal: shell %s found but failed PTY verification", p)
	}
	if len(validated) == 0 {
		logger.Warn("terminal: no shell passed PTY verification, falling back to /bin/sh")
		return "/bin/sh", nil
	}
	return validated[0], validated[1:]
}

func discoverWindowsShell() string {
	candidates := []string{}
	if gitBash := findGitBash(); gitBash != "" {
		candidates = append(candidates, gitBash)
	}
	candidates = append(candidates, "pwsh")
	if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
		candidates = append(candidates, systemRoot+`\System32\WindowsPowerShell\v1.0\powershell.exe`)
	}
	candidates = append(candidates, "powershell", os.Getenv("ComSpec"), "cmd")
	for _, c := range candidates {
		p := resolveShellPath(c)
		if p == "" {
			continue
		}
		if verifyShell(p) {
			return p
		}
		logger.Warn("terminal: shell %s found but failed trial run", p)
	}
	return "cmd.exe"
}

func resolveShellPath(c string) string {
	if c == "" {
		return ""
	}
	if p, err := exec.LookPath(c); err == nil {
		return p
	}
	if _, err := os.Stat(c); err == nil {
		return c
	}
	return ""
}

func verifyShell(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := strings.TrimSuffix(filepath.Base(path), ".exe")
	var cmd *exec.Cmd
	switch base {
	case "cmd":
		cmd = exec.CommandContext(ctx, path, "/c", "exit", "0")
	case "pwsh", "powershell":
		cmd = exec.CommandContext(ctx, path, "-Command", "exit", "0")
	default:
		cmd = exec.CommandContext(ctx, path, "-c", "true")
	}
	return cmd.Run() == nil
}

func findGitBash() string {
	git, err := exec.LookPath("git")
	if err != nil {
		return ""
	}
	dir := filepath.Dir(filepath.Dir(git))
	bash := filepath.Join(dir, "bin", "bash.exe")
	if _, err := os.Stat(bash); err == nil {
		return bash
	}
	return ""
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func reorderFallbacks(candidates []string, selected string) []string {
	ordered := make([]string, 0, len(candidates)-1)
	for _, candidate := range candidates {
		if candidate != selected {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}
