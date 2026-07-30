package localserver

import (
	"context"
	"encoding/json"
	"net/http"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/runtime"
	"cs-cloud/internal/workflow"
	"cs-cloud/internal/workflowrunner"
)

// agentManagerSessionBinder creates a local csc conversation session for a
// workflow task using the default agent managed by the runtime AgentManager.
type agentManagerSessionBinder struct {
	manager *runtime.AgentManager
}

func (b *agentManagerSessionBinder) Bind(ctx context.Context, sessionID, cwd string, env []string, permMode string) error {
	return b.manager.BindWorkflowSession(ctx, sessionID, cwd, env, permMode)
}

func (b *agentManagerSessionBinder) RunSession(ctx context.Context, sessionID, cwd, prompt string, env []string, permMode string) ([]byte, error) {
	return b.manager.RunWorkflowSession(ctx, sessionID, cwd, prompt, env, permMode)
}

func (b *agentManagerSessionBinder) AbortSession(ctx context.Context, sessionID string) error {
	return b.manager.AbortWorkflowSession(ctx, sessionID)
}

var _ workflowrunner.ConversationBinder = (*agentManagerSessionBinder)(nil)
var _ workflowrunner.SessionRunner = (*agentManagerSessionBinder)(nil)
var _ workflowrunner.SessionAborter = (*agentManagerSessionBinder)(nil)

// BindWorkflowSessionBinder wires the workflow driver to the default csc
// agent so it can create local conversation sessions that match the server
// chat session IDs. Call this after the default agent has been initialized.
func (s *Server) BindWorkflowSessionBinder() {
	if s.workflow == nil || s.manager == nil {
		return
	}
	if s.manager.DefaultBackend() != "csc" {
		return
	}
	s.workflow.SetConversationBinder(&agentManagerSessionBinder{manager: s.manager})
}

// handleWorkflowHealth reports whether the workflow driver is healthy.
func (s *Server) handleWorkflowHealth(w http.ResponseWriter, r *http.Request) {
	if s.workflow == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}
	if s.workflowErr != nil {
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", "workflow driver disabled: "+s.workflowErr.Error())
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
// The driver reports status to the server asynchronously.
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
	// Route is POST /workflow/tasks/{id}/run: reject when the path id is present
	// but does not match the body, so /tasks/A/run with body task_id=B cannot
	// silently run B.
	if pathID := r.PathValue("id"); pathID != "" && pathID != payload.TaskID {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "path task id does not match body task_id")
		return
	}

	// A disabled/not-running driver is a 503, not a 409: the caller should
	// see "this device cannot run workflow tasks", not a task conflict.
	if err := s.workflow.Health(); err != nil {
		reason := err.Error()
		if s.workflowErr != nil {
			reason = "workflow driver disabled: " + s.workflowErr.Error()
		}
		writeErr(w, http.StatusServiceUnavailable, "UNAVAILABLE", reason)
		return
	}

	if err := s.workflow.RunTaskAsync(payload); err != nil {
		logger.Warn("workflow: task %s rejected: %v", payload.TaskID, err)
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

// handleWorkflowTaskComplete forwards an agent's explicit "complete task"
// signal to the workflow driver. It is called by the in-task CLI when the
// agent decides its work is done (worker) or reaches a review decision
// (critic). The driver stores the payload and wakes runAgent; it does NOT call
// CompleteTask directly — execute remains the sole owner of task-status
// callbacks. Responds 409 when the task is not running (already finished /
// unknown) so the CLI can surface that to the agent.
func (s *Server) handleWorkflowTaskComplete(w http.ResponseWriter, r *http.Request) {
	if s.workflow == nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "workflow driver not registered")
		return
	}
	taskID := r.PathValue("id")
	if taskID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "missing task id")
		return
	}
	var req struct {
		Action   string `json:"action"`
		Summary  string `json:"summary"`
		Decision string `json:"decision"`
		Reason   string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	sig := agent.CompletionSignal{Action: req.Action, Summary: req.Summary, Decision: req.Decision, Reason: req.Reason}
	if err := s.workflow.SignalTaskCompletion(taskID, sig); err != nil {
		writeErr(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}
	writeOK(w, map[string]string{"status": "accepted"})
}
