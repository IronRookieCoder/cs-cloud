package localserver

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"cs-cloud/internal/logger"
)

// workspaceRoot returns the sandbox root for workspace cleanup. Overridable
// via CS_WORKSPACE_ROOT; defaults to /workspace (the path baked into the
// localserver image). Paths outside this root are refused.
func workspaceRoot() string {
	if v := os.Getenv("CS_WORKSPACE_ROOT"); v != "" {
		return filepath.Clean(v)
	}
	return "/workspace"
}

// resolveUnderRoot ensures abs (after symlink resolution) is a strict
// descendant of root. Returns the resolved absolute path or an error.
func resolveUnderRoot(abs, root string) (string, error) {
	abs = filepath.Clean(abs)
	root = filepath.Clean(root)
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if abs == root {
		return "", errWorkspaceIsRoot
	}
	if !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return "", errPathEscapes
	}
	return abs, nil
}

// handleWorkspaceDelete deletes a single workspace directory.
//
// @Summary      Delete workspace
// @Description  Recursively deletes a workspace directory by path. The path is sandboxed to CS_WORKSPACE_ROOT (default /workspace); the root itself and any path that resolves outside it are refused. Idempotent: deleting a non-existent path returns 200 with existed=false.
// @Tags         workspace
// @Param        X-Workspace-Directory  header  string  false  "Workspace directory to delete"
// @Param        dir                     query   string  false  "Workspace directory to delete (used when header is absent)"
// @Success      200  {object}  envelope
// @Failure      400  {object}  envelope
// @Failure      500  {object}  envelope
// @Router       /workspace [delete]
func (s *Server) handleWorkspaceDelete(w http.ResponseWriter, r *http.Request) {
	dir := getWorkspaceDir(r)
	if dir == "" {
		dir = decodeQueryParam(r.URL.Query().Get("dir"))
	}
	if dir == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "missing workspace directory: set X-Workspace-Directory header or ?dir= query")
		return
	}

	abs, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid workspace directory: "+err.Error())
		return
	}

	root := workspaceRoot()
	resolved, err := resolveUnderRoot(abs, root)
	if err != nil {
		switch err {
		case errWorkspaceIsRoot:
			writeErr(w, http.StatusBadRequest, "refused", "refuse to delete workspace root")
		case errPathEscapes:
			writeErr(w, http.StatusBadRequest, "refused", "path escapes workspace root")
		default:
			writeErr(w, http.StatusBadRequest, "refused", err.Error())
		}
		return
	}

	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Info("workspace delete: idempotent skip (absent): %s", resolved)
			writeOK(w, map[string]any{"deleted": resolved, "existed": false})
			return
		}
		writeErr(w, http.StatusInternalServerError, "stat_failed", err.Error())
		return
	}
	if !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "not_directory", "path exists but is not a directory: "+resolved)
		return
	}

	if err := os.RemoveAll(resolved); err != nil {
		logger.Error("workspace delete failed: %s err: %v", resolved, err)
		writeErr(w, http.StatusInternalServerError, "remove_failed", err.Error())
		return
	}
	logger.Info("workspace deleted: %s", resolved)
	writeOK(w, map[string]any{"deleted": resolved, "existed": true})
}
