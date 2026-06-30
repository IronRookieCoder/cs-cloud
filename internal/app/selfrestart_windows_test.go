//go:build windows

package app

import (
	"strings"
	"testing"
)

func TestBuildUpgradeScript_NoNewWindow(t *testing.T) {
	s := buildUpgradeScript(12345, `C:\tmp\cs-cloud.exe.new`, `C:\app\cs-cloud.exe`, []string{"_daemon", "--port", "8080"})

	if !strings.Contains(s, `start "" /B`) {
		t.Errorf("upgrade script must use 'start \"\" /B' to avoid spawning a window\nscript:\n%s", s)
	}
	// guard against regressing back to bare 'start ""' (without /B)
	if strings.Contains(s, `start "" "`) {
		t.Errorf("upgrade script must not contain bare 'start \"\" \"' (would create a window)\nscript:\n%s", s)
	}
}

func TestBuildUpgradeScript_RendersArgs(t *testing.T) {
	s := buildUpgradeScript(99, `C:\tmp\n.new`, `C:\app\cs-cloud.exe`, []string{"_daemon"})
	if !strings.Contains(s, `"C:\app\cs-cloud.exe" "_daemon"`) {
		t.Errorf("script should embed exe path and args, got:\n%s", s)
	}
	if !strings.Contains(s, `"PID eq 99"`) {
		t.Errorf("script should embed pid, got:\n%s", s)
	}
}
