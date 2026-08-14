// Package autostart registers cs-cloud to launch on user login / boot across
// Windows, macOS and Linux. The desktop flavours (Linux XDG .desktop, macOS
// LaunchAgent, Windows registry Run key) fire after user login; headless
// Linux falls back to a systemd user unit that survives reboots via linger.
package autostart

import (
	"fmt"
	"os"
	"runtime"
)

// commandRunner is the seam used by the systemd branch for test injection:
// the real path runs `systemctl` / `loginctl` via os/exec; tests substitute
// a fake. Declared in the build-tag-free file so both Linux and non-Linux
// compilation units see it.
type commandRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

// Config describes the autostart entry to create.
type Config struct {
	// Name is the internal identifier used for file/unit names (e.g. "cs-cloud").
	Name string
	// DisplayName is the human-readable name shown in desktop entries.
	DisplayName string
	// Exec is the full command: Exec[0] is the executable path, the rest are args.
	Exec []string
}

// Manager controls a single autostart entry across platforms.
type Manager interface {
	// Enable registers the autostart entry.
	Enable() error
	// Disable removes the autostart entry.
	Disable() error
	// Enabled reports whether the entry is currently registered.
	Enabled() (bool, error)
	// Method returns a short identifier of the mechanism in use, e.g.
	// "desktop", "systemd-user", "unsupported".
	Method() string
}

// New picks the best autostart mechanism for the current platform.
func New(cfg Config) (Manager, error) {
	if cfg.Name == "" {
		return nil, fmt.Errorf("autostart: Config.Name is empty")
	}
	if len(cfg.Exec) == 0 {
		return nil, fmt.Errorf("autostart: Config.Exec is empty")
	}
	return newManager(cfg), nil
}

// IsDesktopSession reports whether the current Linux/BSD session has a desktop
// environment that honours XDG autostart entries. Returns true on macOS/Windows
// where a GUI session is the norm.
func IsDesktopSession() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	for _, k := range []string{"XDG_CURRENT_DESKTOP", "DISPLAY", "WAYLAND_DISPLAY"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func newManager(cfg Config) Manager {
	switch runtime.GOOS {
	case "darwin", "windows":
		return newDesktopManager(cfg)
	case "linux":
		if IsDesktopSession() {
			return newDesktopManager(cfg)
		}
		return newSystemdUserManager(cfg)
	default:
		return &unsupportedManager{cfg: cfg, reason: "unsupported OS: " + runtime.GOOS}
	}
}

type unsupportedManager struct {
	cfg    Config
	reason string
}

func (m *unsupportedManager) Enable() error      { return fmt.Errorf("autostart: %s", m.reason) }
func (m *unsupportedManager) Disable() error     { return fmt.Errorf("autostart: %s", m.reason) }
func (m *unsupportedManager) Enabled() (bool, error) { return false, nil }
func (m *unsupportedManager) Method() string     { return "unsupported" }
