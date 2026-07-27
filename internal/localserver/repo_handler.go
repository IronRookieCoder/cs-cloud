package localserver

import (
	"encoding/json"
	"net/http"
)

type repoCheckoutRequest struct {
	TaskID     string `json:"task_id"`
	RepoURL    string `json:"repo_url"`
	BaseBranch string `json:"base_branch,omitempty"`
}

type repoCheckoutResponse struct {
	Path string `json:"path"`
}

// handleRepoCheckout serves an agent's on-demand `cs-cloud repo checkout`:
// creates (or resets) a per-repo branch worktree for a running task and returns
// its filesystem path. Mounted under /api/v1 (see server.go).
func (s *Server) handleRepoCheckout(w http.ResponseWriter, r *http.Request) {
	if s.workflow == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}
	if err := s.workflow.Health(); err != nil {
		reason := err.Error()
		if s.workflowErr != nil {
			reason = "workflow driver disabled: " + s.workflowErr.Error()
		}
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", reason)
		return
	}
	var req repoCheckoutRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if req.TaskID == "" || req.RepoURL == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "task_id and repo_url required")
		return
	}
	path, err := s.workflow.CheckoutRepo(req.TaskID, req.RepoURL, req.BaseBranch)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "CHECKOUT_FAILED", err.Error())
		return
	}
	writeOK(w, repoCheckoutResponse{Path: path})
}
