package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"cs-cloud/internal/logger"
)

type UpgradeState struct {
	PreviousVersion string `json:"previous_version"`
	CurrentVersion  string `json:"current_version"`
	UpgradedAt      string `json:"upgraded_at"`
	BackupPath      string `json:"backup_path"`
	Status          string `json:"status"`
}

type Replacer struct {
	upgradesDir string
}

func NewReplacer(upgradesDir string) *Replacer {
	return &Replacer{upgradesDir: upgradesDir}
}

func (r *Replacer) Replace(currentExe, newBinary, currentVersion, newVersion string) error {
	if err := os.MkdirAll(r.upgradesDir, 0o755); err != nil {
		return fmt.Errorf("create upgrades dir: %w", err)
	}

	backupPath := filepath.Join(r.upgradesDir, "cs-cloud.bak")
	if _, err := os.Stat(backupPath); err == nil {
		os.Remove(backupPath)
	}

	logger.Info("[replacer] backing up current binary: %s -> %s", currentExe, backupPath)
	if err := copyFile(currentExe, backupPath); err != nil {
		return fmt.Errorf("backup current binary: %w", err)
	}

	state := &UpgradeState{
		PreviousVersion: currentVersion,
		CurrentVersion:  newVersion,
		UpgradedAt:      time.Now().UTC().Format(time.RFC3339),
		BackupPath:      backupPath,
		Status:          "pending_verify",
	}
	if err := r.saveState(state); err != nil {
		return fmt.Errorf("save upgrade state: %w", err)
	}

	if runtime.GOOS == "windows" {
		return r.replaceWindows(currentExe, newBinary)
	}
	return r.replaceUnix(currentExe, newBinary)
}

func (r *Replacer) replaceUnix(currentExe, newBinary string) error {
	logger.Info("[replacer] unix: renaming %s -> %s", newBinary, currentExe)
	if err := os.Rename(newBinary, currentExe); err != nil {
		return fmt.Errorf("replace binary: %w", err)
	}
	logger.Info("[replacer] binary replaced successfully")
	return nil
}

func (r *Replacer) replaceWindows(currentExe, newBinary string) error {
	newPath := currentExe + ".new"
	if _, err := os.Stat(newPath); err == nil {
		os.Remove(newPath)
	}
	logger.Info("[replacer] windows: staging new binary -> %s", newPath)
	if err := os.Rename(newBinary, newPath); err != nil {
		return fmt.Errorf("stage new binary: %w", err)
	}

	logger.Info("[replacer] windows: staged %s (swap deferred to restart helper)", newPath)
	return nil
}

func (r *Replacer) Rollback(currentExe string) error {
	backupPath := filepath.Join(r.upgradesDir, "cs-cloud.bak")
	if _, err := os.Stat(backupPath); err != nil {
		return fmt.Errorf("backup not found: %w", err)
	}

	oldPath := currentExe + ".old"
	if _, err := os.Stat(oldPath); err == nil {
		os.Rename(oldPath, currentExe)
	} else {
		os.Rename(backupPath, currentExe)
	}

	os.Remove(currentExe + ".new")
	r.saveState(&UpgradeState{Status: "rolled_back"})
	return nil
}

func (r *Replacer) MarkVerified() error {
	state, err := r.LoadState()
	if err != nil {
		return err
	}
	if state == nil {
		return nil
	}
	state.Status = "completed"
	return r.saveState(state)
}

func (r *Replacer) LoadState() (*UpgradeState, error) {
	p := r.stateFile()
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var state UpgradeState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *Replacer) Cleanup() {
	os.Remove(filepath.Join(r.upgradesDir, "cs-cloud.bak"))
	os.Remove(filepath.Join(r.upgradesDir, "cs-cloud-new"))
}

func (r *Replacer) stateFile() string {
	return filepath.Join(r.upgradesDir, "current.json")
}

func (r *Replacer) saveState(state *UpgradeState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.stateFile(), data, 0o644)
}

func (r *Replacer) AppendHistory(entry *UpgradeState) error {
	if err := os.MkdirAll(r.upgradesDir, 0o755); err != nil {
		return err
	}
	history, _ := r.LoadHistory()
	history = append(history, entry)
	if len(history) > 20 {
		history = history[len(history)-20:]
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.historyFile(), data, 0o644)
}

func (r *Replacer) LoadHistory() ([]*UpgradeState, error) {
	data, err := os.ReadFile(r.historyFile())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var history []*UpgradeState
	if err := json.Unmarshal(data, &history); err != nil {
		return nil, err
	}
	return history, nil
}

func (r *Replacer) historyFile() string {
	return filepath.Join(r.upgradesDir, "history.json")
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
