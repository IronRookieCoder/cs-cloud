package localserver

import (
	"net/http"

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
