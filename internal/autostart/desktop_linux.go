//go:build linux

package autostart

import (
	"os"
	"path/filepath"
	"strings"
)

const desktopTemplate = `[Desktop Entry]
Type=Application
Name={{.DisplayName}}
Exec={{.Exec}}
X-GNOME-Autostart-enabled=true
`

type linuxDesktopManager struct{ cfg Config }

func newDesktopManager(cfg Config) Manager { return &linuxDesktopManager{cfg: cfg} }

func (m *linuxDesktopManager) Method() string { return "desktop" }

func (m *linuxDesktopManager) dir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "autostart")
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".config", "autostart")
}

func (m *linuxDesktopManager) path() string {
	return filepath.Join(m.dir(), m.cfg.Name+".desktop")
}

func (m *linuxDesktopManager) Enabled() (bool, error) {
	_, err := os.Stat(m.path())
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func (m *linuxDesktopManager) Enable() error {
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return err
	}
	content := strings.ReplaceAll(desktopTemplate, "{{.DisplayName}}", escapeDesktop(m.cfg.DisplayName))
	content = strings.ReplaceAll(content, "{{.Exec}}", quoteExec(m.cfg.Exec))
	return os.WriteFile(m.path(), []byte(content), 0o644)
}

func (m *linuxDesktopManager) Disable() error {
	err := os.Remove(m.path())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
