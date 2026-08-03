package localserver

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cs-cloud/internal/config"
)

// helper: build a Server with a given runtime config and call resolvePath with
// a workspace header and a candidate path.
func callResolvePath(t *testing.T, cfg config.RuntimeConfig, workspace, relPath string) (string, string, error) {
	t.Helper()
	s := &Server{runtimeCfg: cfg}
	req := httptest.NewRequest("GET", "/", nil)
	if workspace != "" {
		req.Header.Set(workspaceDirHeader, workspace)
	}
	return s.resolvePath(req, relPath)
}

func TestResolvePathRelativeStaysInWorkspace(t *testing.T) {
	ws := t.TempDir()
	got, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: true}, ws, "src/main.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(ws, "src", "main.go")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolvePathRejectsTraversal(t *testing.T) {
	ws := t.TempDir()
	// "../../etc/passwd" must not escape the workspace even when absolute
	// paths are allowed (the relative path is joined to the workspace).
	_, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: true}, ws, "../../etc/passwd")
	if err == nil {
		t.Fatalf("expected workspace-escape error, got nil")
	}
	if !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolvePathRejectsHighRiskAbsolute(t *testing.T) {
	// Default config: AllowAbsolutePaths=true. Even so, /etc/shadow and
	// equivalent high-risk paths must be refused.
	cases := []string{
		"/etc/shadow",
		"/etc/ssh/ssh_host_rsa_key",
		"/home/u/.ssh/id_rsa",
		"/home/u/.aws/credentials",
		"/home/u/.kube/config",
		"/home/u/.docker/config.json",
		"/home/u/.netrc",
		"/home/u/.claude/settings.json",
	}
	for _, p := range cases {
		_, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: true}, t.TempDir(), p)
		if err == nil {
			t.Fatalf("expected denial for high-risk path %q, got nil", p)
		}
		if !strings.Contains(err.Error(), "not permitted") {
			t.Fatalf("for %q: unexpected error %v", p, err)
		}
	}
}

func TestResolvePathRejectsHighRiskOnWindowsDrives(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-specific path forms")
	}
	cases := []string{
		`C:\Windows\System32\config\SAM`,
		`C:\Users\Admin\.ssh\id_rsa`,
		`C:\Users\Admin\AppData\Roaming\Microsoft\Credentials\abc`,
	}
	for _, p := range cases {
		_, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: true}, t.TempDir(), p)
		if err == nil {
			t.Fatalf("expected denial for windows high-risk path %q, got nil", p)
		}
	}
}

func TestResolvePathRejectsSymlinkEscape(t *testing.T) {
	ws := t.TempDir()
	// Create a sibling directory outside the workspace containing a secret.
	secretRoot := filepath.Join(filepath.Dir(ws), "outside-"+filepath.Base(ws))
	if err := os.MkdirAll(secretRoot, 0o755); err != nil {
		t.Fatalf("mkdir secret root: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(secretRoot) })

	// Inside the workspace, plant a symlink that points outside. Use a name
	// that is NOT on the blacklist, so the only thing that should block this
	// is the symlink-resolved containment check.
	link := filepath.Join(ws, "escape")
	target := filepath.Join(secretRoot, "data.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		// Symlink creation may require privileges on some Windows setups;
		// skip rather than fail in that case.
		t.Skipf("symlink unsupported in this environment: %v", err)
	}

	// sandboxed path (AllowAbsolutePaths=false): must be rejected because the
	// resolved real path is outside the workspace.
	_, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: false}, ws, "escape")
	if err == nil {
		t.Fatalf("expected symlink-escape error, got nil")
	}
}

func TestResolvePathRejectsSymlinkToHighRisk(t *testing.T) {
	ws := t.TempDir()
	// Create a symlink inside the workspace that points at a high-risk
	// location. Even with AllowAbsolutePaths=true (sandbox off), the blacklist
	// applied to the resolved real path must catch this.
	target := "/etc/ssh"
	if runtime.GOOS == "windows" {
		target = `C:\Users\Public\.ssh`
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Skipf("cannot prepare windows target: %v", err)
		}
	}
	link := filepath.Join(ws, "keys")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported in this environment: %v", err)
	}

	// Pass the absolute symlink path so sandboxing is OFF (AllowAbsolutePaths
	// is true), which means the containment check is skipped — the only thing
	// that should block this request is the blacklist applied to the resolved
	// real path. A relative relPath would keep the sandbox on and the test
	// would measure the wrong defense.
	_, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: true}, ws, link)
	if err == nil {
		t.Fatalf("expected denial for symlink to high-risk location, got nil")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestResolvePathAllowsBenignAbsoluteBrowse(t *testing.T) {
	// The legitimate front-end use case: browsing an arbitrary directory on
	// the device to pick a workspace. Pick a benign absolute path that is NOT
	// on the blacklist and verify it resolves.
	ws := t.TempDir()
	target := t.TempDir() // a second tempdir we know is benign
	got, _, err := callResolvePath(t, config.RuntimeConfig{AllowAbsolutePaths: true}, ws, target)
	if err != nil {
		t.Fatalf("expected benign absolute path to resolve, got: %v", err)
	}
	if got != target {
		t.Fatalf("got %q, want %q", got, target)
	}
}

func TestPathIsHighRiskSegmentMatching(t *testing.T) {
	cases := map[string]bool{
		// directory + nested file
		"/home/u/.ssh/id_rsa": true,
		// directory itself
		"/home/u/.ssh": true,
		// case-insensitive
		"/HOME/U/.SSH/ID_RSA": true,
		// windows slash-normalized
		`C:\Users\Admin\.ssh\id_rsa`: true,
		// must NOT match a directory whose name merely ends with ".ssh"
		"/home/u/foo.ssh/data": false,
		// must NOT match ".ssh-backup"
		"/home/u/.ssh-backup/id_rsa": false,
		// /etc/shadow exact
		"/etc/shadow": true,
		// /etc/shadow- (different file) — netrc uses Contains so this WOULD
		// match "/.netrc" as a substring. Acceptable: such siblings are also
		// typically secret-bearing. We assert true here to document the
		// behavior.
		"/home/u/.netrc.bak": true,
		// benign
		"/home/u/projects/repo/main.go": false,
	}
	for path, want := range cases {
		got := pathIsHighRisk(path)
		if got != want {
			t.Errorf("pathIsHighRisk(%q) = %v, want %v", path, got, want)
		}
	}
}
