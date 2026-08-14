package autostart

import "strings"

// quoteExec renders an Exec line for a .desktop file per the freedesktop
// Desktop Entry Spec: arguments containing spaces, tabs, quotes or backslashes
// are double-quoted with escaping. The same quoting is reused for systemd
// ExecStart lines because both specs delegate to the same rule.
func quoteExec(args []string) string {
	out := make([]string, len(args))
	for i, s := range args {
		if strings.ContainsAny(s, " \t\"\\") {
			s = strings.ReplaceAll(s, "\\", "\\\\")
			s = strings.ReplaceAll(s, "\"", "\\\"")
			s = "\"" + s + "\""
		}
		out[i] = s
	}
	return strings.Join(out, " ")
}

// escapeDesktop sanitises free-form display name fields. Newlines and carriage
// returns would break .desktop key=value parsing and could inject unit keys
// into the systemd template.
func escapeDesktop(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}

// systemdUnitTemplate is a user-level unit (systemctl --user). It targets
// default.target so it launches on the user's systemd instance at login;
// for boot-time start (no login) the user must additionally enable linger
// via `loginctl enable-linger $USER`. After=network-online.target +
// Wants= expresses the online dependency that cs-cloud needs to reach the
// cloud gateway.
const systemdUnitTemplate = `[Unit]
Description={{.Description}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart={{.ExecStart}}
Restart=on-failure
RestartSec=3

[Install]
WantedBy=default.target
`
