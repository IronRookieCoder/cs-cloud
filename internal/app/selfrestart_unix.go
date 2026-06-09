//go:build !windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"cs-cloud/internal/logger"
)

func newDaemonCmd(exe string, args []string) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

func selfRestartWithUpgrade(a *App, exe, newPath string) error {
	logger.Info("[selfrestart] unix: swapping binary %s -> %s", newPath, exe)
	if err := os.Rename(newPath, exe); err != nil {
		return fmt.Errorf("swap binary: %w", err)
	}

	args := a.LoadArgs()
	if len(args) == 0 {
		args = []string{"_daemon"}
	}

	if err := a.SaveState("restarting"); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	cmd := newDaemonCmd(exe, args)
	if err := cmd.Start(); err != nil {
		a.SaveState("running")
		return fmt.Errorf("start new process: %w", err)
	}

	logger.Info("[selfrestart] new process started (pid=%d), exiting current", cmd.Process.Pid)
	os.Exit(0)
	return nil
}
