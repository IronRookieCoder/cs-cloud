package localserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type commandRequest struct {
	CommandID string          `json:"command_id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
}

type commandAck struct {
	CommandID string `json:"command_id"`
	Status    string `json:"status"`
	Message   string `json:"message,omitempty"`
}

type commandStatusResponse struct {
	CommandID   string    `json:"command_id"`
	Type        string    `json:"type"`
	Status      string    `json:"status"`
	StartedAt   string    `json:"started_at,omitempty"`
	CompletedAt string    `json:"completed_at,omitempty"`
	Result      any       `json:"result,omitempty"`
	Error       string    `json:"error,omitempty"`
	Phase       string    `json:"phase,omitempty"`
	Progress    float64   `json:"progress,omitempty"`
	Message     string    `json:"message,omitempty"`
}

// @Summary      Dispatch a command
// @Description  Dispatches a remote command (upgrade, restart, reconnect) for asynchronous execution.
// @Tags         Command
// @Accept       json
// @Produce      json
// @Param        body  body  commandRequest  true  "Command request"
// @Success      200  {object}  envelope{data=commandAck}
// @Failure      400  {object}  envelope
// @Failure      409  {object}  envelope
// @Failure      503  {object}  envelope
// @Router       /commands [post]
func (s *Server) handleCommandDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	var req commandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "invalid json: "+err.Error())
		return
	}

	if req.CommandID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "command_id is required")
		return
	}

	validTypes := map[string]bool{"upgrade": true, "restart": true, "reconnect": true, "restart-agent": true}
	if !validTypes[req.Type] {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("unknown command type: %s", req.Type))
		return
	}

	if s.dispatcher == nil {
		writeErr(w, http.StatusServiceUnavailable, "NO_DISPATCHER", "command dispatcher not available")
		return
	}

	ack, err := s.dispatcher.Dispatch(r.Context(), &req)
	if err != nil {
		writeErr(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}

	writeOK(w, ack)
}

// @Summary      Get command status
// @Description  Returns the execution status of a dispatched command.
// @Tags         Command
// @Produce      json
// @Param        command_id  query  string  true  "Command ID"
// @Success      200  {object}  envelope{data=commandStatusResponse}
// @Failure      400  {object}  envelope
// @Failure      404  {object}  envelope
// @Failure      503  {object}  envelope
// @Router       /commands/status [get]
func (s *Server) handleCommandStatus(w http.ResponseWriter, r *http.Request) {
	commandID := r.URL.Query().Get("command_id")
	if commandID == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "command_id is required")
		return
	}

	if s.dispatcher == nil {
		writeErr(w, http.StatusServiceUnavailable, "NO_DISPATCHER", "command dispatcher not available")
		return
	}

	status, err := s.dispatcher.Status(commandID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}

	writeOK(w, status)
}

func writeCommandAck(commandID, status, message string) *commandAck {
	return &commandAck{
		CommandID: commandID,
		Status:    status,
		Message:   message,
	}
}

func writeCommandStatus(cmd *commandRequest, status, errMsg string, startedAt, completedAt time.Time, result any) *commandStatusResponse {
	resp := &commandStatusResponse{
		CommandID:   cmd.CommandID,
		Type:        cmd.Type,
		Status:      status,
		Result:      result,
	}
	if !startedAt.IsZero() {
		resp.StartedAt = startedAt.Format(time.RFC3339)
	}
	if !completedAt.IsZero() {
		resp.CompletedAt = completedAt.Format(time.RFC3339)
	}
	if errMsg != "" {
		resp.Error = errMsg
	}
	return resp
}

func writeCommandStatusWithProgress(cmd *commandRequest, status, errMsg string, startedAt, completedAt time.Time, result any, phase string, progress float64, message string) *commandStatusResponse {
	resp := writeCommandStatus(cmd, status, errMsg, startedAt, completedAt, result)
	resp.Phase = phase
	resp.Progress = progress
	resp.Message = message
	return resp
}
