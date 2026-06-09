//go:build windows

package app

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"cs-cloud/internal/logger"
)

func newDaemonCmd(exe string, args []string) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000,
		HideWindow:    true,
	}
	return cmd
}

func selfRestartWithUpgrade(a *App, exe, newPath string) error {
	args := a.LoadArgs()
	if len(args) == 0 {
		args = []string{"_daemon"}
	}

	if err := a.SaveState("restarting"); err != nil {
		return fmt.Errorf("save state: %w", err)
	}

	pid := os.Getpid()

	var argsBuf strings.Builder
	for _, arg := range args {
		argsBuf.WriteString(" \"")
		argsBuf.WriteString(arg)
		argsBuf.WriteString("\"")
	}

	script := fmt.Sprintf(`@echo off
:wait_pid
tasklist /FI "PID eq %d" /NH 2>NUL | findstr /C:"%d" >NUL
if not errorlevel 1 (
    timeout /t 1 /nobreak >NUL
    goto wait_pid
)
:swap
timeout /t 1 /nobreak >NUL
move /y "%s" "%s"
if errorlevel 1 (
    goto swap
)
start "" "%s"%s
del "%%~f0"
`, pid, pid, newPath, exe, exe, argsBuf.String())

	scriptPath := exe + ".upgrade.cmd"
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return fmt.Errorf("write upgrade script: %w", err)
	}

	cmd := exec.Command("cmd.exe", "/c", scriptPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000,
		HideWindow:    true,
	}
	if err := cmd.Start(); err != nil {
		a.SaveState("running")
		return fmt.Errorf("start upgrade script: %w", err)
	}

	logger.Info("[selfrestart] upgrade helper launched (pid=%d), exiting current process", cmd.Process.Pid)
	logger.Sync()
	logger.Close()
	os.Exit(0)
	return nil
}
