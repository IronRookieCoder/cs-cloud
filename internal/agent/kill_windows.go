//go:build windows

package agent

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func SetCmdProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x08000000,
		HideWindow:    true,
	}
}

func SignalTerminate(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	_ = proc.Signal(os.Interrupt)
}

func KillProcessTree(pid int) {
	cmd := exec.Command("taskkill", "/pid", fmt.Sprintf("%d", pid), "/f", "/t")
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	_ = cmd.Run()
}
