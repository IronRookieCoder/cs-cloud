package localserver

import (
	"net/http"
	"strconv"

	"cs-cloud/internal/agent"
)

// handleRuntimeEventList serves GET /api/v1/runtime/events. It returns the
// ring buffer slice with timestamp >= ?since=<ms>, optionally filtered by
// ?conversation_id=<id>. Response is a JSON array of agent.Event objects
// in chronological (insertion) order.
//
// Designed for csc TUI polling: the client passes its last-seen timestamp
// as ?since and consumes the returned batch, draining pending replies.
func (s *Server) handleRuntimeEventList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET")
		return
	}
	if s.ringBuffer == nil {
		// Ring buffer not initialised (server constructed without one).
		// Return an empty array rather than 500 so callers can degrade gracefully.
		writeJSON(w, http.StatusOK, []agent.Event{})
		return
	}

	var sinceMs int64
	if raw := r.URL.Query().Get("since"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed < 0 {
			writeErr(w, http.StatusBadRequest, "BAD_SINCE", `"since" must be a non-negative Unix millisecond timestamp`)
			return
		}
		sinceMs = parsed
	}
	conversationID := r.URL.Query().Get("conversation_id")

	events := s.ringBuffer.Query(sinceMs, conversationID)
	writeJSON(w, http.StatusOK, events)
}
