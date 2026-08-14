package localserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cs-cloud/internal/config"
)

// callEnsureCwd drives the auto-mkdir path with a synthetic POST
// /api/v1/conversations request and the given Server config. Tests assert on
// side effects (whether the target directory was actually created) since the
// function returns nothing and only logs.
func callEnsureCwd(t *testing.T, cfg config.RuntimeConfig, rootDir, header, bodyJSON string) {
	t.Helper()
	s := &Server{runtimeCfg: cfg, rootDir: rootDir}
	var body []byte
	if bodyJSON != "" {
		body = []byte(bodyJSON)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/conversations", bytes.NewReader(body))
	if header != "" {
		req.Header.Set(workspaceDirHeader, header)
	}
	s.ensureConversationWorkdir(req)
}

// TestEnsureCwdCreatesLegitimateSubdir exercises the happy path: a fresh path
// under the configured anchor must be created when AllowAbsolutePaths is off.
func TestEnsureCwdCreatesLegitimateSubdir(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "new-project")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: false}, root, target, "")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected dir created, got: %v", err)
	}
}

// TestEnsureCwdAllowsAbsoluteBenign verifies that with AllowAbsolutePaths
// enabled, a benign absolute path outside any anchor is still honored — the
// front-end workspace-picker flow depends on this.
func TestEnsureCwdAllowsAbsoluteBenign(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "picked-cwd")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: true}, "", target, "")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected dir created, got: %v", err)
	}
}

// TestEnsureCwdRejectsHighRiskHeader plants a blacklisted segment (.ssh)
// inside the anchor so containment would pass — the only thing that should
// block creation is the secret blacklist.
func TestEnsureCwdRejectsHighRiskHeader(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".ssh", "evil")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: false}, root, target, "")
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("expected dir NOT created (blacklist), but it exists")
	}
}

// TestEnsureCwdRejectsHighRiskBody routes the same attack through the body
// cwd JSON fallback instead of the header.
func TestEnsureCwdRejectsHighRiskBody(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".aws", "evil")
	body, _ := json.Marshal(map[string]string{"cwd": target})
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: false}, root, "", string(body))
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("expected dir NOT created (blacklist via body cwd), but it exists")
	}
}

// TestEnsureCwdRejectsHighRiskEvenWithAbsolutePaths confirms the blacklist
// still fires when AllowAbsolutePaths is on (sandbox containment skipped).
func TestEnsureCwdRejectsHighRiskEvenWithAbsolutePaths(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, ".ssh", "evil")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: true}, root, target, "")
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("expected dir NOT created (blacklist with absolute paths on), but it exists")
	}
}

// TestEnsureCwdRejectsEscapeFromAnchor ensures a path outside the anchor is
// refused when AllowAbsolutePaths is off.
func TestEnsureCwdRejectsEscapeFromAnchor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "escaped")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: false}, root, target, "")
	if _, err := os.Stat(target); err == nil {
		t.Fatalf("expected dir NOT created (escapes anchor), but it exists")
	}
}

// TestEnsureCwdExistingDirIsNoop documents that an existing target is left
// alone (no error, no recreation, no mode change).
func TestEnsureCwdExistingDirIsNoop(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "existing")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: false}, root, target, "")
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		t.Fatalf("expected existing dir untouched, got: %v / isDir=%v", err, info != nil && info.IsDir())
	}
}

// TestEnsureCwdEmptyCwdIsNoop guards the early return when neither header nor
// body supplies a cwd.
func TestEnsureCwdEmptyCwdIsNoop(t *testing.T) {
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: true}, "", "", "")
}

// TestEnsureCwdRejectsSymlinkToHighRisk plants a symlink inside the anchor
// that points at a blacklisted location (a sibling dir whose path includes
// /.ssh/), then requests a subdir under the symlink. The lexical form looks
// benign; only the resolveForCreate-resolved ancestor check should catch it.
func TestEnsureCwdRejectsSymlinkToHighRisk(t *testing.T) {
	root := t.TempDir()
	sensitiveBase := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root)+"-ssh")
	sensitiveDir := filepath.Join(sensitiveBase, ".ssh")
	t.Cleanup(func() { _ = os.RemoveAll(sensitiveBase) })
	if err := os.MkdirAll(sensitiveDir, 0o755); err != nil {
		t.Fatalf("mkdir sensitive: %v", err)
	}
	link := filepath.Join(root, "keys")
	if err := os.Symlink(sensitiveDir, link); err != nil {
		t.Skipf("symlink unsupported in this environment: %v", err)
	}
	// AllowAbsolutePaths=true so containment is OFF — the only thing that
	// should block this is the blacklist applied to the resolved real path.
	target := filepath.Join(link, "new")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: true}, root, target, "")
	created := filepath.Join(sensitiveDir, "new")
	if _, err := os.Stat(created); err == nil {
		t.Fatalf("expected symlink-escape to high-risk blocked, but dir was created at %s", created)
	}
}

// TestEnsureCwdRejectsSymlinkEscapeFromAnchor plants a symlink inside the
// anchor pointing OUTSIDE the anchor, then requests a subdir under the
// symlink with AllowAbsolutePaths=false. The lexical form lives inside the
// anchor, so a lexical-only containment check would pass — the only thing
// that should block creation here is the strict resolved-form containment.
func TestEnsureCwdRejectsSymlinkEscapeFromAnchor(t *testing.T) {
	anchor := t.TempDir()
	outside := filepath.Join(filepath.Dir(anchor), "outside-"+filepath.Base(anchor)+"-escape")
	t.Cleanup(func() { _ = os.RemoveAll(outside) })
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	link := filepath.Join(anchor, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unsupported in this environment: %v", err)
	}
	target := filepath.Join(link, "pwned")
	callEnsureCwd(t, config.RuntimeConfig{AllowAbsolutePaths: false}, anchor, target, "")
	created := filepath.Join(outside, "pwned")
	if _, err := os.Stat(created); err == nil {
		t.Fatalf("expected symlink-escape from anchor blocked, but dir was created at %s", created)
	}
}

// TestResolveForCreateResolvesExistingAncestor pins the helper's contract:
// the lexical form is preserved exactly, and the resolved form reflects the
// symlink target of the longest existing ancestor plus the non-existent tail.
func TestResolveForCreateResolvesExistingAncestor(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	realTarget := filepath.Join(root, "real")
	if err := os.MkdirAll(realTarget, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realTarget, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	abs := filepath.Join(link, "new", "deep")
	lexical, resolved := resolveForCreate(abs)
	if lexical != filepath.Clean(abs) {
		t.Fatalf("lexical: got %q, want %q", lexical, filepath.Clean(abs))
	}
	wantResolved := filepath.Join(realTarget, "new", "deep")
	if resolved != wantResolved {
		t.Fatalf("resolved: got %q, want %q", resolved, wantResolved)
	}
}
