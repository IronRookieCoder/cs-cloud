package localserver

import (
	"encoding/json"
	"net/http"

	"cs-cloud/internal/workflow"
)

// handleWorkflowHealth reports whether the workflow driver is healthy.
func (s *Server) handleWorkflowHealth(w http.ResponseWriter, r *http.Request) {
	if s.workflow == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}
	if err := s.workflow.Health(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "ok"})
}

// handleWorkflowTaskRun accepts a task payload and dispatches it to the
// workflow driver, responding as soon as the task is reserved — the agent
// run itself can take minutes and the gateway proxy caps requests at ~30s.
// The driver reports status to multica asynchronously.
func (s *Server) handleWorkflowTaskRun(w http.ResponseWriter, r *http.Request) {
	if s.workflow == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}

	var payload workflow.TaskRunPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	if payload.TaskID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "missing task_id")
		return
	}

	if err := s.workflow.RunTaskAsync(payload); err != nil {
		writeErr(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "accepted"})
}

// handleWorkflowTaskAbort cancels a running workflow task.
func (s *Server) handleWorkflowTaskAbort(w http.ResponseWriter, r *http.Request) {
	if s.workflow == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}

	taskID := r.PathValue("id")
	if taskID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "missing task id")
		return
	}

	if err := s.workflow.AbortTask(taskID); err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "aborted"})
}
