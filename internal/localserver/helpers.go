package localserver

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func readJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	defer r.Body.Close()
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, v)
}

func decodeQueryParam(v string) string {
	if decoded, err := url.QueryUnescape(v); err == nil {
		return decoded
	}
	return v
}

func generateID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}

const workspaceDirHeader = "X-Workspace-Directory"

func getWorkspaceDir(r *http.Request) string {
	v := r.Header.Get(workspaceDirHeader)
	if decoded, err := url.PathUnescape(v); err == nil {
		return decoded
	}
	return v
}

// highRiskPathPatterns lists filesystem locations whose contents must never be
// readable through this API, regardless of AllowAbsolutePaths. These hold
// long-lived secrets (private keys, cloud credentials, system password hashes,
// DPAPI master keys) that an attacker could exfiltrate via the
// read / file-meta / diff handlers.
//
// Entries are matched case-insensitively against the slash-normalized absolute
// path (see pathIsHighRisk). Directory entries carry a trailing slash; file
// entries do not. Patterns include a leading slash so they match as path
// segments rather than anywhere in the string (so "/.ssh/" does not match a
// directory literally named "foo.ssh").
var highRiskPathPatterns = []string{
	// SSH / PGP / host identity
	"/.ssh/",
	"/.gnupg/",
	"/.shosts",
	"/etc/shadow",
	"/etc/gshadow",
	"/etc/ssh/",
	// Cloud provider credentials
	"/.aws/",
	"/.azure/",
	"/.kube/",
	"/.config/gcloud/",
	"/.config/azure/",
	"/.config/doctl/",
	"/.config/linode-cli/",
	"/.config/heroku/",
	"/.docker/config.json",
	// Package registry tokens
	"/.netrc",
	"/.pypirc",
	"/.gem/credentials",
	"/.cargo/credentials",
	"/.npmrc",
	// AI tool credentials & local agent state
	"/.config/github-copilot/",
	"/.claude/",
	"/.costrict/",
	"/.cursor/",
	"/.continue/",
	// Windows credential stores & system hives (paths are slash-normalized
	// before matching, so backslash-forms collapse to these).
	"/appdata/roaming/microsoft/credentials/",
	"/appdata/roaming/microsoft/vault/",
	"/appdata/roaming/microsoft/protect/",
	"/appdata/local/microsoft/credentials/",
	"/windows/system32/config/",
}

// pathIsHighRisk reports whether the resolved absolute path touches a known
// credential/secret location. path must be absolute.
func pathIsHighRisk(path string) bool {
	n := filepath.ToSlash(path)
	n = strings.ReplaceAll(n, `\`, "/")
	n = strings.ToLower(n)
	if !strings.HasSuffix(n, "/") {
		// Append a trailing separator so directory-style patterns (e.g.
		// "/.ssh/") also match when the request names the directory itself
		// rather than a file inside it.
		n += "/"
	}
	for _, p := range highRiskPathPatterns {
		if strings.Contains(n, p) {
			return true
		}
	}
	return false
}

// pathInDir reports whether path is dir itself or a descendant of dir. Both
// arguments should be absolute and filepath.Clean'd by the caller; the check
// is lexical only (no symlink resolution) so callers that need to defend
// against symlink escape must EvalSymlinks first and call again on the
// resolved form.
func pathInDir(path, dir string) bool {
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// resolveForCreate returns both the lexical and symlink-resolved forms of an
// absolute path that is about to be created (e.g. via os.MkdirAll). Since the
// path itself does not exist yet, filepath.EvalSymlinks fails on it directly;
// instead this walks up to the longest existing ancestor, resolves that, and
// reconstructs the non-existent tail. Callers can then apply pathIsHighRisk
// and pathInDir to both forms, closing the symlink-escape gap for mkdir-style
// operations the same way resolvePath closes it for reads.
//
// If no ancestor exists at all (not even the volume root), both return values
// fall back to the lexical form — callers degrade to lexical-only checks.
func resolveForCreate(abs string) (lexical, resolved string) {
	lexical = filepath.Clean(abs)
	existing := lexical
	for {
		if _, err := os.Stat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			// Reached the volume root without a Stat success; give up and
			// let callers fall back to lexical-only checks.
			return lexical, lexical
		}
		existing = parent
	}
	real, err := filepath.EvalSymlinks(existing)
	if err != nil {
		return lexical, lexical
	}
	tail := strings.TrimPrefix(lexical, existing)
	resolved = filepath.Clean(real + tail)
	return lexical, resolved
}

func (s *Server) resolvePath(r *http.Request, relPath string) (absPath string, workspace string, err error) {
	workspace = getWorkspaceDir(r)
	if workspace == "" {
		workspace, _ = os.Getwd()
	}
	workspace = filepath.Clean(workspace)
	if !filepath.IsAbs(workspace) {
		workspace, err = filepath.Abs(workspace)
		if err != nil {
			return "", "", fmt.Errorf("invalid workspace directory: %w", err)
		}
	}
	// Compute the symlink-resolved form of the workspace for the resolved-form
	// containment check below. We do NOT replace `workspace` with this — the
	// lexical form is what we return to callers, so the API keeps emitting
	// paths in the same shape the client supplied (on Windows, EvalSymlinks
	// rewrites 8.3 short names like SXF-AD~1 to the long form SXF-Admin, which
	// would otherwise leak through and break lexical equality elsewhere).
	resolvedWorkspace := workspace
	if resolved, linkErr := filepath.EvalSymlinks(workspace); linkErr == nil {
		resolvedWorkspace = resolved
	}

	// sandboxed=true means the request must stay within the workspace. When
	// AllowAbsolutePaths is on and the client supplied an absolute path, the
	// front-end is browsing the device to pick a workspace, so we deliberately
	// relax containment — but the high-risk blacklist below still applies.
	sandboxed := !(filepath.IsAbs(relPath) && s.runtimeCfg.AllowAbsolutePaths)
	if sandboxed {
		absPath = filepath.Clean(filepath.Join(workspace, relPath))
		if !strings.HasPrefix(absPath, workspace+string(filepath.Separator)) && absPath != workspace {
			return "", "", fmt.Errorf("path escapes workspace directory")
		}
	} else {
		absPath = filepath.Clean(relPath)
	}

	// Resolve symlinks in the final path so a workspace-internal symlink that
	// points outside (e.g. repo/config -> /etc) can't escape the sandbox, and
	// so the blacklist sees the real destination. Non-existent paths fall
	// through with the lexical form — handlers will surface a 404.
	resolvedAbs := absPath
	absResolved := false
	if real, linkErr := filepath.EvalSymlinks(absPath); linkErr == nil {
		resolvedAbs = real
		absResolved = true
	}
	if sandboxed && absResolved {
		// Strict resolved-form containment: the resolved absPath must live
		// inside the resolved workspace. We only run this when absPath itself
		// resolved — a non-existent path can't be symlink-followed by the
		// downstream read, and on Windows EvalSymlinks rewrites 8.3 short
		// names, so comparing a lexical-only absPath against the resolved
		// workspace would falsely flag legitimate requests. The lexical
		// containment check above already covers the non-existent case.
		if !strings.HasPrefix(resolvedAbs, resolvedWorkspace+string(filepath.Separator)) && resolvedAbs != resolvedWorkspace {
			return "", "", fmt.Errorf("path escapes workspace directory")
		}
	}

	// Always deny high-risk credential/secret locations, regardless of how the
	// path was constructed or whether sandboxing is active. Blocks both direct
	// absolute-path requests (e.g. GET /runtime/files/content?path=/etc/shadow)
	// and symlink-based escapes that land in a sensitive directory.
	if pathIsHighRisk(absPath) || pathIsHighRisk(resolvedAbs) {
		return "", "", fmt.Errorf("access to this path is not permitted")
	}

	return absPath, workspace, nil
}
