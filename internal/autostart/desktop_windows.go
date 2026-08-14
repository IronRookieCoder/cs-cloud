//go:build windows

package autostart

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// windowsDesktopManager installs a per-user Run key under HKCU. The Run key
// is walked by Windows' login process to launch per-user startup tasks.
// We use HKCU (not HKLM) so no elevation is required; cs-cloud is a
// per-user daemon.
type windowsDesktopManager struct{ cfg Config }

// runKeyPath is the well-known registry path Windows' login process walks
// to launch per-user startup tasks.
const runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

func newDesktopManager(cfg Config) Manager { return &windowsDesktopManager{cfg: cfg} }

func (m *windowsDesktopManager) Method() string { return "registry-run" }

func (m *windowsDesktopManager) Enabled() (bool, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return false, nil
		}
		return false, err
	}
	defer k.Close()
	_, _, err = k.GetStringValue(m.cfg.Name)
	if err == registry.ErrNotExist {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (m *windowsDesktopManager) Enable() error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	cmd := renderWindowsCommand(m.cfg.Exec)
	return k.SetStringValue(m.cfg.Name, cmd)
}

func (m *windowsDesktopManager) Disable() error {
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	defer k.Close()
	if err := k.DeleteValue(m.cfg.Name); err != nil {
		if err == registry.ErrNotExist {
			return nil
		}
		return err
	}
	return nil
}

// renderWindowsCommand composes the REG_SZ value for the Run key. The
// Windows CreateProcess rule: an executable path containing spaces and no
// surrounding quotes is resolved ambiguously ("C:\Program Files\foo.exe"
// may resolve to C:\Program.exe first). Always quote the exe; quote any
// arg containing spaces; leave args that already carry a leading quote
// untouched so callers can pass pre-quoted flags.
func renderWindowsCommand(args []string) string {
	if len(args) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(quoteIfSpaces(args[0]))
	for _, a := range args[1:] {
		b.WriteByte(' ')
		b.WriteString(quoteIfSpaces(a))
	}
	return b.String()
}

// quoteIfSpaces wraps s in double quotes if it contains a space or tab and
// isn't already quoted. Empty strings emit a literal "".
func quoteIfSpaces(s string) string {
	if s == "" {
		return `""`
	}
	if s[0] == '"' {
		return s
	}
	if strings.ContainsAny(s, " \t") {
		return `"` + s + `"`
	}
	return s
}
