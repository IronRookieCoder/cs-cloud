package cli

import (
	"fmt"
	"os"
	"runtime"

	"cs-cloud/internal/app"
	"cs-cloud/internal/autostart"
	"cs-cloud/internal/platform"
)

// autostartCmd dispatches enable|disable|status for the boot-launch entry.
//
// cs-cloud autostart enable   - register the entry (per-platform)
// cs-cloud autostart disable  - remove the entry
// cs-cloud autostart status   - report current state
//
// The Exec line is built to match `cs-cloud _daemon` semantics — the
// foreground daemon entry — so a registered systemd/LaunchAgent/.desktop
// entry stays alive under Type=simple instead of being marked dead on the
// fork that `cs-cloud start` performs.
func autostartCmd(a *app.App, args []string) error {
	sub := "enable"
	if len(args) > 0 {
		sub = args[0]
	}

	cfg, err := buildAutostartConfig(a)
	if err != nil {
		return err
	}
	mgr, err := autostart.New(cfg)
	if err != nil {
		return err
	}

	switch sub {
	case "enable":
		return enableAutostart(mgr, cfg)
	case "disable":
		return disableAutostart(mgr)
	case "status":
		return statusAutostart(mgr, cfg)
	default:
		printUsage()
		return fmt.Errorf("unknown autostart subcommand: %s (want enable|disable|status)", sub)
	}
}

// buildAutostartConfig assembles the Exec vector the same way start.go
// builds the daemon fork args, so a manually-started daemon and an
// autostarted one read identical configuration (auth path, data dir,
// port, host, upgrade flag).
func buildAutostartConfig(a *app.App) (autostart.Config, error) {
	exe, err := os.Executable()
	if err != nil {
		return autostart.Config{}, fmt.Errorf("resolve executable: %w", err)
	}

	exec := []string{exe, "_daemon"}
	if p := platform.AuthPath(); p != "" {
		exec = append(exec, "--auth-path", p)
	}
	if d := platform.DataDir(); d != "" {
		exec = append(exec, "--data-dir", d)
	}
	if port, _ := parsePort(); port > 0 {
		exec = append(exec, "--port", fmt.Sprintf("%d", port))
	}
	if h, err := parseHost(); err == nil && h != "" {
		exec = append(exec, "--host", h)
	}
	if platform.NoAutoUpgrade() {
		exec = append(exec, "--no-auto-upgrade")
	}

	return autostart.Config{
		Name:        "cs-cloud",
		DisplayName: "cs-cloud daemon",
		Exec:        exec,
	}, nil
}

func enableAutostart(mgr autostart.Manager, cfg autostart.Config) error {
	if err := mgr.Enable(); err != nil {
		printError("Failed to enable autostart: %v", err)
		return err
	}
	printSuccess("Autostart enabled (method: %s)", mgr.Method())

	if exe := cfg.Exec[0]; exe != "" {
		if _, statErr := os.Stat(exe); statErr != nil {
			printWarn("Registered executable no longer exists at %s — re-enable after moving the binary", exe)
		}
	}

	// On headless Linux the user systemd instance won't start at boot
	// without linger. Detect + advise, but never auto-sudo.
	if runtime.GOOS == "linux" && mgr.Method() == "systemd-user" {
		adviseLinger()
	}
	printInfo("Use 'autostart status' to verify")
	return nil
}

func disableAutostart(mgr autostart.Manager) error {
	if err := mgr.Disable(); err != nil {
		printError("Failed to disable autostart: %v", err)
		return err
	}
	printSuccess("Autostart disabled (method: %s)", mgr.Method())
	return nil
}

func statusAutostart(mgr autostart.Manager, cfg autostart.Config) error {
	enabled, err := mgr.Enabled()
	if err != nil {
		printError("Failed to query autostart: %v", err)
		return err
	}
	printSection("Autostart")
	printKV("method", mgr.Method())
	if enabled {
		printKV("state", "enabled")
	} else {
		printKV("state", "disabled")
	}
	if exe := cfg.Exec[0]; exe != "" {
		if _, statErr := os.Stat(exe); statErr != nil {
			printWarn("Registered executable missing at %s — re-enable after restoring the binary", exe)
		}
	}
	if runtime.GOOS == "linux" && mgr.Method() == "systemd-user" {
		adviseLinger()
	}
	return nil
}

// adviseLinger prints a one-shot warning when the current user's systemd
// instance won't survive boot without linger. Failures reading loginctl
// fall through silently — we only advise when we have positive evidence
// that linger is off.
func adviseLinger() {
	status, err := autostart.LingerStatus(nil)
	if err != nil {
		return
	}
	if status != "yes" {
		user := os.Getenv("USER")
		if user == "" {
			user = "$USER"
		}
		printWarn("Boot-time start needs linger (currently %q). Run: sudo loginctl enable-linger %s", status, user)
	}
}
