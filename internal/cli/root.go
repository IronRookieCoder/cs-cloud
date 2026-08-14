package cli

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"cs-cloud/internal/app"
	"cs-cloud/internal/platform"
)

func Execute() error {
	parseGlobalFlags()

	a, err := app.New()
	if err != nil {
		return err
	}

	return dispatch(a)
}

func parseGlobalFlags() {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--auth-path" && i+1 < len(args):
			platform.SetAuthPath(args[i+1])
			i++
		case len(args[i]) > 11 && args[i][:11] == "--auth-path=":
			platform.SetAuthPath(args[i][11:])
		case args[i] == "--data-dir" && i+1 < len(args):
			platform.SetDataDir(args[i+1])
			i++
		case len(args[i]) > 11 && args[i][:11] == "--data-dir=":
			platform.SetDataDir(args[i][11:])
		case args[i] == "--agent-path" && i+1 < len(args):
			setAgentPathEnv(args[i+1])
			i++
		case len(args[i]) > 13 && args[i][:13] == "--agent-path=":
			setAgentPathEnv(args[i][13:])
		case args[i] == "--no-auto-upgrade":
			platform.SetNoAutoUpgrade(true)
		}
	}
}

// setAgentPathEnv propagates --agent-path into the env so config.Load() picks
// it up. Writes both the new CS_BRIDGE_AGENT_PATH name and the legacy
// CS_CLOUD_AGENT_PATH alias for cross-version compatibility.
func setAgentPathEnv(v string) {
	os.Setenv("CS_BRIDGE_AGENT_PATH", v)
	os.Setenv("CS_CLOUD_AGENT_PATH", v)
}

func commandArgs() []string {
	var rest []string
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--auth-path" && i+1 < len(args):
			i++
		case len(args[i]) > 11 && args[i][:11] == "--auth-path=":
		case args[i] == "--data-dir" && i+1 < len(args):
			i++
		case len(args[i]) > 11 && args[i][:11] == "--data-dir=":
		case (args[i] == "--mode" || args[i] == "-m") && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--mode="):
		case strings.HasPrefix(args[i], "-m="):
		case (args[i] == "--port" || args[i] == "-p") && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--port="):
		case strings.HasPrefix(args[i], "-p="):
		case (args[i] == "--host") && i+1 < len(args):
			i++
		case strings.HasPrefix(args[i], "--host="):
		case args[i] == "--no-auto-upgrade":
		case args[i] == "--agent-path" && i+1 < len(args):
			i++
		case len(args[i]) > 13 && args[i][:13] == "--agent-path=":
		case args[i] == "--json", strings.HasPrefix(args[i], "--json="):
		default:
			rest = append(rest, args[i])
		}
	}
	return rest
}

func parseMode() string {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		switch {
		case (args[i] == "--mode" || args[i] == "-m") && i+1 < len(args):
			return args[i+1]
		case strings.HasPrefix(args[i], "--mode="):
			return args[i][7:]
		case strings.HasPrefix(args[i], "-m="):
			return args[i][3:]
		}
	}
	return "cloud"
}

func parseHost() (string, error) {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		var raw string
		switch {
		case (args[i] == "--host") && i+1 < len(args):
			raw = args[i+1]
		case strings.HasPrefix(args[i], "--host="):
			raw = args[i][7:]
		}
		if raw == "" {
			continue
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return "", fmt.Errorf("invalid host: empty value")
		}
		if _, _, err := net.SplitHostPort(raw); err == nil {
			return "", fmt.Errorf("invalid host %q: should not contain a port, use --port separately", raw)
		}
		if net.ParseIP(raw) == nil && !isValidHostname(raw) {
			return "", fmt.Errorf("invalid host %q: must be a valid IP address or hostname", raw)
		}
		return raw, nil
	}
	return "127.0.0.1", nil
}

// isValidHostname performs a basic check that the string looks like a
// DNS hostname (RFC 952/1123). It is intentionally lenient and does not
// enforce a strict length limit; the OS resolver has the final say.
func isValidHostname(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	labels := strings.Split(s, ".")
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for i, c := range label {
			if c >= 'a' && c <= 'z' {
				continue
			}
			if c >= 'A' && c <= 'Z' {
				continue
			}
			if c >= '0' && c <= '9' {
				continue
			}
			if c == '-' && i > 0 && i < len(label)-1 {
				continue
			}
			return false
		}
	}
	return true
}

func parsePort() (int, error) {
	args := os.Args[1:]
	for i := 0; i < len(args); i++ {
		var raw string
		switch {
		case (args[i] == "--port" || args[i] == "-p") && i+1 < len(args):
			raw = args[i+1]
		case strings.HasPrefix(args[i], "--port="):
			raw = args[i][7:]
		case strings.HasPrefix(args[i], "-p="):
			raw = args[i][3:]
		}
		if raw == "" {
			continue
		}
		port, err := strconv.Atoi(raw)
		if err != nil || port < 0 || port > 65535 {
			return 0, fmt.Errorf("invalid port: %s", raw)
		}
		return port, nil
	}
	return 0, nil
}

func dispatch(a *app.App) error {
	cmds := commandArgs()
	if len(cmds) == 0 {
		printUsage()
		return nil
	}

	switch cmds[0] {
	case "start":
		return start(a)
	case "stop":
		return stop(a)
	case "restart":
		return restart(a)
	case "status":
		return status(a)
	case "logs":
		return logs(a)
	case "logf":
		return logf(a)
	case "doctor":
		return doctor(a)
	case "register":
		return register(a)
	case "login":
		return login(a)
	case "logout":
		return logout(a)
	case "version":
		printVersion(a)
		return nil
	case "upgrade":
		return upgradeCmd(a)
	case "serve":
		return serve(a)
	case "gc":
		return gc(a)
	case "workflow":
		return workflowCmd(a, cmds[1:])
	case "task":
		return memberTaskCmd(a, cmds[1:])
	case "autostart":
		return autostartCmd(a, cmds[1:])
	case "_daemon":
		return runDaemon(a)
	case "help", "-h", "--help":
		printUsage()
		return nil
	default:
		printUsage()
		return fmt.Errorf("unknown command: %s", cmds[0])
	}
}

func printUsage() {
	printTitle("cs-cloud")
	printSection("Usage")
	fmt.Println(dimStyle.Render("  cs-cloud [flags] <command>"))

	printSection("Flags")
	fmt.Print(renderKV([][2]string{
		{"--auth-path", "Path to auth.json (default: ~/.costrict/share/auth.json)"},
		{"--data-dir", "Base data directory (default: ~/.costrict)"},
		{"--mode, -m", "Daemon mode: cloud (default) or local"},
		{"--port, -p", "Local server port (default: random available port)"},
		{"--host", "Bind address for the local server (default: 127.0.0.1, use 0.0.0.0 to accept all connections)"},
		{"--no-auto-upgrade", "Disable auto-upgrade on cloud command"},
	}))

	printSection("Commands")
	cmds := [][2]string{
		{"version", "Show version info"},
		{"upgrade", "Check and apply upgrades"},
		{"start", "Start daemon (cloud mode with WS tunnel, or --mode local)"},
		{"stop", "Stop daemon"},
		{"restart", "Restart daemon"},
		{"status", "Show daemon status"},
		{"logs", "Show daemon logs"},
		{"logf", "Tail daemon logs (follow mode)"},
		{"doctor", "Diagnose and auto-fix common issues"},
		{"register", "Register device"},
		{"login", "Login via browser OAuth"},
		{"logout", "Delete credentials and device info"},
		{"serve", "Run server in foreground (no daemon)"},
		{"gc", "Remove expired attachments and report freed bytes"},
		{"workflow", "Manage workflow resources"},
		{"task", "Process assigned member tasks locally"},
		{"autostart", "Enable/disable boot autostart (enable|disable|status)"},
	}
	fmt.Print(renderKV(cmds))
}
