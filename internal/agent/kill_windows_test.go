//go:build windows

package agent

import (
	"os/exec"
	"syscall"
	"testing"
)

func TestSetCmdProcessGroup_HidesWindow(t *testing.T) {
	cmd := exec.Command("cmd", "/c", "echo hi")
	SetCmdProcessGroup(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr not set")
	}

	const createNoWindow = 0x08000000
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NO_WINDOW (0x08000000)", cmd.SysProcAttr.CreationFlags)
	}
	if cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x, missing CREATE_NEW_PROCESS_GROUP", cmd.SysProcAttr.CreationFlags)
	}
	if !cmd.SysProcAttr.HideWindow {
		t.Errorf("HideWindow = false, want true")
	}
}
