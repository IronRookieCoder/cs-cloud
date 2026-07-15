package localserver

import (
	"encoding/json"
	"net/http"

	workflowagent "cs-cloud/internal/agent/workflow"
	"cs-cloud/internal/workflow"
)

// handleWorkflowHealth reports whether the workflow driver is healthy.
func (s *Server) handleWorkflowHealth(w http.ResponseWriter, r *http.Request) {
	d, ok := s.manager.GetPersistentDriver("workflow")
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}
	if err := d.Health(); err != nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "ok"})
}

// handleWorkflowTaskRun accepts a task payload and dispatches it to the
// workflow driver. The driver reports status to multica asynchronously.
func (s *Server) handleWorkflowTaskRun(w http.ResponseWriter, r *http.Request) {
	d, ok := s.manager.GetPersistentDriver("workflow")
	if !ok {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}

	var payload workflow.TaskRunPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	tr, ok := d.(*workflowagent.Driver)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", "driver does not support tasks")
		return
	}

	if err := tr.RunTask(payload); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "started"})
}
