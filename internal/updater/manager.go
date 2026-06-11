package updater

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"cs-cloud/internal/logger"
	"cs-cloud/internal/version"
)

type Policy int

const (
	PolicyAuto     Policy = iota
	PolicyDownload
	PolicyManual
)

type ProgressFunc func(phase string, progress float64, message string)

type Manager struct {
	checker      *Checker
	downloader   *Downloader
	verifier     *Verifier
	replacer     *Replacer
	policy       Policy
	interval     time.Duration
	autoCheck    bool
	upgradesDir  string
	mu           sync.Mutex
	running      bool
	lastCheck    time.Time
	lastResult   *CheckResult
	RestartCh    chan struct{}
}

type Option func(*Manager)

func WithPolicy(p Policy) Option {
	return func(m *Manager) { m.policy = p }
}

func WithInterval(d time.Duration) Option {
	return func(m *Manager) { m.interval = d }
}

func WithAutoCheck(v bool) Option {
	return func(m *Manager) { m.autoCheck = v }
}

func NewManager(cloudBaseURL, rootDir string, opts ...Option) *Manager {
	upgradesDir := filepath.Join(rootDir, "upgrades")
	exe, _ := os.Executable()
	exeDir := ""
	if exe != "" {
		exeDir = filepath.Dir(exe)
	}
	m := &Manager{
		checker:     NewChecker(cloudBaseURL),
		downloader:  NewDownloader(filepath.Join(exeDir, ".cs-cloud-update")),
		verifier:    NewVerifier(),
		replacer:    NewReplacer(upgradesDir),
		policy:      PolicyAuto,
		interval:    6 * time.Hour,
		upgradesDir: upgradesDir,
		RestartCh:   make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func (m *Manager) Run(ctx context.Context) {
	m.verifyOnStartup()

	if m.autoCheck {
		m.doCheck(ctx)
	}
}

func (m *Manager) DidVerifyOnStartup() bool {
	state, err := m.replacer.LoadState()
	if err != nil || state == nil {
		return false
	}
	if state.Status != "completed" {
		return false
	}
	t, err := time.Parse(time.RFC3339, state.UpgradedAt)
	if err != nil {
		return false
	}
	return time.Since(t) < 2*time.Minute
}

func (m *Manager) CheckNow(ctx context.Context) (*CheckResult, error) {
	return m.checker.Check(ctx)
}

func (m *Manager) Apply(ctx context.Context, targetVersion string) error {
	return m.ApplyWithProgress(ctx, targetVersion, nil)
}

func (m *Manager) ApplyWithProgress(ctx context.Context, targetVersion string, onProgress ProgressFunc) error {
	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return fmt.Errorf("upgrade already in progress")
	}
	m.running = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
	}()

	if onProgress != nil {
		onProgress("checking", 5, "Checking for updates...")
	}

	result, err := m.checker.Check(ctx)
	if err != nil {
		return fmt.Errorf("check: %w", err)
	}
	if !result.CanUpdate {
		return fmt.Errorf("no update available")
	}
	if targetVersion != "" && result.Version != targetVersion {
		return fmt.Errorf("available version %s does not match requested %s", result.Version, targetVersion)
	}

	return m.executeUpgradeWithProgress(ctx, result, onProgress)
}

func (m *Manager) SwapStagedBinary() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve exe symlink: %w", err)
	}
	return m.replacer.SwapStagedBinary(exe)
}

func (m *Manager) Rollback() error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve exe symlink: %w", err)
	}
	if err := m.replacer.Rollback(exe); err != nil {
		return err
	}
	logger.Info("rollback completed")
	return nil
}

func (m *Manager) History() (*UpgradeState, error) {
	return m.replacer.LoadState()
}

func (m *Manager) FullHistory() ([]*UpgradeState, error) {
	return m.replacer.LoadHistory()
}

func (m *Manager) LastCheck() (time.Time, *CheckResult) {
	return m.lastCheck, m.lastResult
}

func (m *Manager) doCheck(ctx context.Context) {
	result, err := m.checker.Check(ctx)
	if err != nil {
		logger.Error("update check failed: %v", err)
		return
	}
	m.lastCheck = time.Now()
	m.lastResult = result

	if !result.CanUpdate {
		logger.Info("no update available (current: %s)", version.Get())
		return
	}

	logger.Info("update available: %s → %s (force: %v)", version.Get(), result.Version, result.Force)

	if m.policy == PolicyManual {
		return
	}

	m.mu.Lock()
	if m.running {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
		}()
		if m.policy == PolicyDownload {
			logger.Info("update downloaded, waiting for manual apply (version: %s)", result.Version)
			_, err := m.downloadAndVerify(ctx, result)
			if err != nil {
				logger.Error("download failed: %v", err)
			}
			return
		}
		if err := m.executeUpgrade(ctx, result); err != nil {
			logger.Error("auto upgrade failed: %v", err)
		}
	}()
}

func (m *Manager) executeUpgrade(ctx context.Context, result *CheckResult) error {
	return m.executeUpgradeWithProgress(ctx, result, nil)
}

func (m *Manager) executeUpgradeWithProgress(ctx context.Context, result *CheckResult, onProgress ProgressFunc) error {
	logger.Info("starting upgrade to %s (force=%v)", result.Version, result.Force)

	newBinary, err := m.downloadAndVerifyWithProgress(ctx, result, onProgress)
	if err != nil {
		return err
	}

	if onProgress != nil {
		onProgress("replacing", 90, "Installing...")
	}

	exe, err := os.Executable()
	if err != nil {
		cleanupFile(newBinary)
		return fmt.Errorf("resolve exe: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		cleanupFile(newBinary)
		return fmt.Errorf("resolve exe symlink: %w", err)
	}

	logger.Info("replacing binary: %s -> %s", version.Get(), result.Version)
	if err := m.replacer.Replace(exe, newBinary, version.Get(), result.Version); err != nil {
		cleanupFile(newBinary)
		return fmt.Errorf("replace: %w", err)
	}

	if onProgress != nil {
		onProgress("restarting", 95, "Restarting...")
	}

	logger.Info("upgrade to %s completed, requesting restart", result.Version)
	select {
	case m.RestartCh <- struct{}{}:
	default:
	}
	return nil
}

func (m *Manager) downloadAndVerify(ctx context.Context, result *CheckResult) (string, error) {
	return m.downloadAndVerifyWithProgress(ctx, result, nil)
}

func (m *Manager) downloadAndVerifyWithProgress(ctx context.Context, result *CheckResult, onProgress ProgressFunc) (string, error) {
	if result.DownloadURL == "" {
		return "", fmt.Errorf("no download url in check result")
	}

	start := time.Now()
	logger.Info("downloading %s from %s", result.Version, result.DownloadURL)

	var path string
	var err error
	if onProgress != nil {
		onProgress("downloading", 10, fmt.Sprintf("Downloading %s...", result.Version))
		downloadProgress := func(wrote, total int64) {
			if total <= 0 {
				return
			}
			pct := 10 + float64(wrote)*70/float64(total)
			onProgress("downloading", pct, fmt.Sprintf("Downloading %s...", result.Version))
		}
		path, err = m.downloader.DownloadWithProgress(ctx, result.DownloadURL, result.SHA256, downloadProgress)
	} else {
		path, err = m.downloader.Download(ctx, result.DownloadURL, result.SHA256)
	}
	if err != nil {
		return "", fmt.Errorf("download: %w", err)
	}

	if onProgress != nil {
		onProgress("verifying", 85, "Verifying integrity...")
	}

	if fi, fiErr := os.Stat(path); fiErr == nil {
		logger.Info("download complete: %d bytes in %s (sha256 ok)", fi.Size(), time.Since(start).Round(time.Millisecond))
	} else {
		logger.Info("download verified (sha256 ok) in %s", time.Since(start).Round(time.Millisecond))
	}
	return path, nil
}

func (m *Manager) verifyOnStartup() {
	state, err := m.replacer.LoadState()
	if err != nil || state == nil {
		return
	}
	if state.Status != "pending_verify" {
		return
	}

	logger.Info("verifying pending upgrade %s → %s", state.PreviousVersion, state.CurrentVersion)

	if state.CurrentVersion != version.Get() {
		logger.Error("version mismatch after upgrade, expected %s but running %s, rolling back", state.CurrentVersion, version.Get())
		exe, _ := os.Executable()
		if exe != "" {
			exe, _ = filepath.EvalSymlinks(exe)
			if rerr := m.replacer.Rollback(exe); rerr != nil {
				logger.Error("rollback failed: %v", rerr)
			}
		}
		return
	}

	if err := m.replacer.MarkVerified(); err != nil {
		logger.Error("mark verified failed: %v", err)
		return
	}

	m.replacer.AppendHistory(&UpgradeState{
		PreviousVersion: state.PreviousVersion,
		CurrentVersion:  state.CurrentVersion,
		UpgradedAt:      state.UpgradedAt,
		Status:          "completed",
	})

	m.replacer.Cleanup()
	logger.Info("upgrade to %s verified successfully", state.CurrentVersion)
}

func cleanupFile(path string) {
	if path != "" {
		os.Remove(path)
	}
}

func PlatformString() string {
	return fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)
}
