package app

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"cs-cloud/internal/logger"
)

func SelfRestart(a *App) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve exe: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Errorf("resolve exe symlink: %w", err)
	}

	newPath := exe + ".new"
	if _, err := os.Stat(newPath); err == nil {
		return selfRestartWithUpgrade(a, exe, newPath)
	}

	args := a.LoadArgs()
	if len(args) == 0 {
		args = []string{"_daemon"}
	}

	if err := a.SaveState("restarting"); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	logger.Info("[selfrestart] launching new process: %s %v", exe, args)
	logger.Sync()
	logger.Close()

	cmd := newRestartCmd(exe, args)
	nullFd, nullErr := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if nullErr == nil {
		cmd.Stdout = nullFd
		cmd.Stderr = nullFd
	}

	if err := cmd.Start(); err != nil {
		a.SaveState("running")
		return fmt.Errorf("start new process: %w", err)
	}

	newPid := cmd.Process.Pid
	logger.Info("[selfrestart] new process started (pid=%d), waiting for readiness...", newPid)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-time.After(500 * time.Millisecond):
		}
		if !a.IsProcessRunning(newPid) {
			a.SaveState("running")
			return fmt.Errorf("[selfrestart] new process (pid=%d) exited unexpectedly", newPid)
		}
		running, state, _ := a.IsRunning()
		if running && state == "running" {
			logger.Info("[selfrestart] new process (pid=%d) is ready, exiting current", newPid)
			os.Exit(0)
		}
	}

	logger.Warn("[selfrestart] new process (pid=%d) did not become ready within timeout, exiting current anyway", newPid)
	os.Exit(0)
	return nil
}

func newRestartCmd(exe string, args []string) *exec.Cmd {
	return newDaemonCmd(exe, args)
}
