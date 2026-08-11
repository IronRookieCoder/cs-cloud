//go:build darwin

package autostart

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// launchAgentTemplate follows the LaunchAgent plist shape macOS' launchd
// expects: RunAtLoad fires the job on user login, AbandonProcessGroup
// prevents launchd from killing the daemon's children when the agent
// itself exits or restarts. KeepAlive is intentionally off — cs-cloud
// manages its own lifecycle (heartbeats, tunnels, restarter).
const launchAgentTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>
    <key>ProgramArguments</key>
    <array>
{{.ProgramArguments}}
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>AbandonProcessGroup</key>
    <true/>
</dict>
</plist>
`

type darwinDesktopManager struct{ cfg Config }

func newDesktopManager(cfg Config) Manager { return &darwinDesktopManager{cfg: cfg} }

func (m *darwinDesktopManager) Method() string { return "launchagent" }

func (m *darwinDesktopManager) dir() string {
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

func (m *darwinDesktopManager) path() string {
	return filepath.Join(m.dir(), m.cfg.Name+".plist")
}

func (m *darwinDesktopManager) Enabled() (bool, error) {
	_, err := os.Stat(m.path())
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (m *darwinDesktopManager) Enable() error {
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return err
	}
	content := strings.ReplaceAll(launchAgentTemplate, "{{.Label}}", escapePlistString(m.cfg.Name))
	content = strings.ReplaceAll(content, "{{.ProgramArguments}}", renderProgramArguments(m.cfg.Exec))
	return os.WriteFile(m.path(), []byte(content), 0o644)
}

func (m *darwinDesktopManager) Disable() error {
	err := os.Remove(m.path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// renderProgramArguments builds the <string>...</string> entries inside the
// ProgramArguments <array>. Each arg is escaped per plist string rules.
func renderProgramArguments(args []string) string {
	var b strings.Builder
	for _, a := range args {
		fmt.Fprintf(&b, "        <string>%s</string>\n", escapePlistString(a))
	}
	return b.String()
}

// escapePlistString escapes XML special chars. Newlines inside args would
// break plist parsing, so collapse them to spaces too.
func escapePlistString(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
