package localserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"cs-cloud/internal/updater"
	"cs-cloud/internal/version"
)

type updateCheckData struct {
	CurrentVersion string `json:"current_version"`
	CanUpdate      bool   `json:"can_update"`
	Version        string `json:"version,omitempty"`
	Changelog      string `json:"changelog,omitempty"`
	Force          bool   `json:"force,omitempty"`
	ReleaseDate    string `json:"release_date,omitempty"`
	BinarySize     int64  `json:"size,omitempty"`
}

// handleUpdateCheck checks the cloud server for available updates.
func (s *Server) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if s.updateChecker == nil {
		writeOK(w, updateCheckData{
			CurrentVersion: s.version,
		})
		return
	}

	result, err := s.updateChecker.Check(r.Context())
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, "UPDATE_CHECK_FAILED", err.Error())
		return
	}

	writeOK(w, updateCheckData{
		CurrentVersion: version.Get(),
		CanUpdate:      result.CanUpdate,
		Version:        result.Version,
		Changelog:      result.Changelog,
		Force:          result.Force,
		ReleaseDate:    result.ReleaseDate,
		BinarySize:     result.BinarySize,
	})
}

func (s *Server) SetUpdateChecker(c *updater.Checker) {
	s.updateChecker = c
}

type updateApplyRequest struct {
	Version string `json:"version,omitempty"`
}

// @Summary      Trigger upgrade
// @Description  Triggers an asynchronous upgrade. Returns a command_id to poll progress via GET /commands/status.
// @Tags         Runtime
// @Accept       json
// @Produce      json
// @Param        body  body  updateApplyRequest  false  "Optional target version"
// @Success      200  {object}  envelope{data=commandAck}
// @Failure      409  {object}  envelope
// @Failure      503  {object}  envelope
// @Router       /runtime/update/apply [post]
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if s.dispatcher == nil {
		writeErr(w, http.StatusServiceUnavailable, "NO_DISPATCHER", "command dispatcher not available")
		return
	}

	var body updateApplyRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	var payload json.RawMessage
	if body.Version != "" {
		payload, _ = json.Marshal(map[string]string{"version": body.Version})
	}

	commandID := fmt.Sprintf("local-%d", time.Now().UnixMilli())
	req := &commandRequest{
		CommandID: commandID,
		Type:      "upgrade",
		Payload:   payload,
	}

	ack, err := s.dispatcher.Dispatch(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusConflict, "CONFLICT", err.Error())
		return
	}

	writeOK(w, ack)
}
