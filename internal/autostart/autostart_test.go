package autostart

import (
	"runtime"
	"strings"
	"testing"
)

// renderSystemdUnit builds the unit body the way Enable() would, but as a
// pure function — extracted here so the test does not touch the filesystem
// or systemctl. Kept in lockstep with systemdUserManager.Enable.
func renderSystemdUnit(cfg Config) string {
	content := strings.ReplaceAll(systemdUnitTemplate, "{{.Description}}", escapeDesktop(cfg.DisplayName))
	content = strings.ReplaceAll(content, "{{.ExecStart}}", quoteExec(cfg.Exec))
	return content
}

func TestRenderSystemdUnit_ExecStartMono(t *testing.T) {
	cfg := Config{
		Name:        "cs-cloud",
		DisplayName: "cs-cloud daemon",
		Exec:        []string{"/usr/local/bin/cs-cloud", "_daemon", "--port", "8080"},
	}
	body := renderSystemdUnit(cfg)

	if !strings.Contains(body, "Type=simple") {
		t.Errorf("missing Type=simple\n---\n%s", body)
	}
	if !strings.Contains(body, "WantedBy=default.target") {
		t.Errorf("missing WantedBy=default.target\n---\n%s", body)
	}
	if !strings.Contains(body, "After=network-online.target") {
		t.Errorf("missing After=network-online.target\n---\n%s", body)
	}
	if !strings.Contains(body, "Wants=network-online.target") {
		t.Errorf("missing Wants=network-online.target\n---\n%s", body)
	}
	if !strings.Contains(body, "Restart=on-failure") {
		t.Errorf("missing Restart=on-failure\n---\n%s", body)
	}
	if !strings.Contains(body, "ExecStart=/usr/local/bin/cs-cloud _daemon --port 8080") {
		t.Errorf("ExecStart line wrong\n---\n%s", body)
	}
}

func TestRenderSystemdUnit_ExecStartQuoting(t *testing.T) {
	cfg := Config{
		Name:        "cs-cloud",
		DisplayName: "cs-cloud",
		Exec:        []string{"/opt/cs cloud/cs-cloud", "_daemon", "--data-dir", "/home/user/co strict"},
	}
	body := renderSystemdUnit(cfg)
	// Paths with spaces must be quoted per freedesktop quoting; backslashes
	// in Windows-style paths would be doubled but here paths are Unix.
	want := `ExecStart="/opt/cs cloud/cs-cloud" _daemon --data-dir "/home/user/co strict"`
	if !strings.Contains(body, want) {
		t.Errorf("expected ExecStart with quoting %q\n---\n%s", want, body)
	}
}

func TestRenderSystemdUnit_DisplayNameSanitized(t *testing.T) {
	// A malicious display name with embedded newlines could inject a new
	// [Install] section. escapeDesktop must flatten to spaces so the
	// injected text stays inside Description.
	cfg := Config{
		Name:        "cs-cloud",
		DisplayName: "cs-cloud\n[Install]\nWantedBy=multi-user.target",
		Exec:        []string{"/usr/bin/cs-cloud", "_daemon"},
	}
	body := renderSystemdUnit(cfg)
	installHeaders := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.TrimSpace(line) == "[Install]" {
			installHeaders++
		}
	}
	if installHeaders != 1 {
		t.Errorf("newline injection added %d [Install] section headers (want 1)\n---\n%s", installHeaders, body)
	}
}

func TestQuoteExec_NoSpecialChars(t *testing.T) {
	got := quoteExec([]string{"a", "b", "c"})
	if got != "a b c" {
		t.Errorf("got %q want %q", got, "a b c")
	}
}

func TestQuoteExec_SpaceArg(t *testing.T) {
	got := quoteExec([]string{"/path with space/exe", "plain", "/path with space"})
	want := `"/path with space/exe" plain "/path with space"`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestQuoteExec_BackslashAndQuote(t *testing.T) {
	got := quoteExec([]string{`C:\Program Files\cs-cloud`, `--flag="v"`})
	// Windows-style path under freedesktop quoting gets backslashes doubled
	// and embedded quotes backslash-escaped, then the whole arg wrapped.
	want := `"C:\\Program Files\\cs-cloud" "--flag=\"v\""`
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestEscapeDesktop_Newlines(t *testing.T) {
	in := "line1\nline2\rline3"
	got := escapeDesktop(in)
	if got != "line1 line2 line3" {
		t.Errorf("got %q want %q", got, "line1 line2 line3")
	}
}

// fakeRunner captures shell invocations for deterministic assertion and
// injects canned stdout/stderr + error.
type fakeRunner struct {
	calls    []fakeCall
	response map[string]fakeResp
}

type fakeCall struct {
	name string
	args []string
}

type fakeResp struct {
	out []byte
	err error
}

func (f *fakeRunner) Run(name string, args ...string) ([]byte, error) {
	key := name + " " + strings.Join(args, " ")
	f.calls = append(f.calls, fakeCall{name: name, args: append([]string(nil), args...)})
	if r, ok := f.response[key]; ok {
		return r.out, r.err
	}
	return []byte{}, nil
}

func TestIsDesktopSession_LinuxNoEnv(t *testing.T) {
	// Force Linux path by clearing the desktop env vars. On a non-Linux
	// host this still exercises the env-walk loop because IsDesktopSession
	// short-circuits true on darwin/windows.
	t.Setenv("XDG_CURRENT_DESKTOP", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	// On darwin/windows we can't reach the false branch; only assert on linux.
	if isLinuxTestHost() {
		if IsDesktopSession() {
			t.Errorf("expected false when no desktop env set")
		}
	}
}

func TestIsDesktopSession_LinuxWithDisplay(t *testing.T) {
	t.Setenv("DISPLAY", ":0")
	if !IsDesktopSession() {
		t.Errorf("expected true when DISPLAY set")
	}
}

// isLinuxTestHost returns true when the test process is running on Linux.
// Used to gate assertions that depend on runtime.GOOS.
func isLinuxTestHost() bool {
	return runtime.GOOS == "linux"
}
