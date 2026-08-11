//go:build linux

package autostart

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

// osExecRunner is the production commandRunner that shells out via
// os/exec. Tests inject their own runner.
type osExecRunner struct{}

func (osExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).Output()
}

type systemdUserManager struct {
	cfg   Config
	shell commandRunner
}

func newSystemdUserManager(cfg Config) Manager {
	return &systemdUserManager{cfg: cfg, shell: osExecRunner{}}
}

func (m *systemdUserManager) Method() string { return "systemd-user" }

func (m *systemdUserManager) unitName() string { return m.cfg.Name + ".service" }

func (m *systemdUserManager) dir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "systemd", "user")
	}
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return filepath.Join(home, ".config", "systemd", "user")
}

func (m *systemdUserManager) path() string {
	return filepath.Join(m.dir(), m.unitName())
}

func (m *systemdUserManager) Enabled() (bool, error) {
	out, err := m.shell.Run("systemctl", "--user", "is-enabled", m.unitName())
	// systemctl returns non-zero when the unit is masked, disabled, or
	// nonexistent. Stdout content is the discriminator ("enabled",
	// "disabled", "static", "masked", etc.).
	if err != nil {
		// Honor a unit file that exists on disk but was never `enable`d:
		// report false so callers know to run enable.
		if strings.TrimSpace(string(out)) == "disabled" {
			return false, nil
		}
		return false, nil
	}
	return strings.TrimSpace(string(out)) == "enabled", nil
}

func (m *systemdUserManager) Enable() error {
	if err := os.MkdirAll(m.dir(), 0o755); err != nil {
		return err
	}
	content := strings.ReplaceAll(systemdUnitTemplate, "{{.Description}}", escapeDesktop(m.cfg.DisplayName))
	content = strings.ReplaceAll(content, "{{.ExecStart}}", quoteExec(m.cfg.Exec))
	if err := os.WriteFile(m.path(), []byte(content), 0o644); err != nil {
		return err
	}
	if _, err := m.shell.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	if _, err := m.shell.Run("systemctl", "--user", "enable", m.unitName()); err != nil {
		return fmt.Errorf("systemctl enable: %w", err)
	}
	return nil
}

func (m *systemdUserManager) Disable() error {
	if _, err := m.shell.Run("systemctl", "--user", "disable", m.unitName()); err != nil {
		// Tolerate a missing unit: if disable says "not loaded", we still
		// want to clean the file. Re-check existence below.
		if !strings.Contains(err.Error(), "No such file or directory") {
			return fmt.Errorf("systemctl disable: %w", err)
		}
	}
	if err := os.Remove(m.path()); err != nil && !os.IsNotExist(err) {
		return err
	}
	if _, err := m.shell.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	return nil
}

// LingerStatus reports whether the user's systemd instance is configured to
// start at boot (independent of login). Returns the raw string ("yes"/"no"
// /"") plus an error if loginctl is unavailable.
func LingerStatus(r commandRunner) (string, error) {
	if r == nil {
		r = osExecRunner{}
	}
	u := currentUserName()
	out, err := r.Run("loginctl", "show-user", u, "--property=Linger")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Linger=") {
			return strings.TrimSpace(strings.TrimPrefix(line, "Linger=")), nil
		}
	}
	return "", nil
}

func currentUserName() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	if u := os.Getenv("LOGNAME"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}
