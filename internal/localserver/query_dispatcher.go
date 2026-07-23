package localserver

import (
	"net/http"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

// handlePermissionList dispatches GET /permissions.
//
// Per design decision 3.1 (GET 反查分流):
//   - TUI registry has ≥1 live permission.asked entry: return them flattened
//     to {"id": "...", "sessionID": "...", ...} objects (200 OK), matching
//     the bare-array shape SaaS's parseIDList expects (id at top level, not
//     nested inside an agent.Event envelope). The csc TUI client is the only
//     consumer of TUI-sourced permissions; cloud dispatcher traffic is routed
//     through a separate path. handleProxy is NOT called when TUI has pending.
//   - TUI registry is empty: delegate to handleProxy unchanged. Behaviour
//     for pre-Phase-2 callers stays byte-identical (csc-serve returns its own
//     pending list in its own envelope shape).
//
// The two paths are mutually exclusive in practice: dual-ownership protection
// in TUIRegistry.Register prevents the same permissionID from being tracked
// by both the TUI registry and csc-serve, so a GET will never need to merge
// the two sources.
//
// @Summary      List pending permissions
// @Description  Dispatches by source: returns TUI-sourced pending permissions when present, otherwise proxies to the agent backend.
// @Tags         Permission
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /permissions [get]
func (s *Server) handlePermissionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET")
		return
	}
	if s.tuiRegistry != nil {
		if pending := s.tuiRegistry.PendingPermissions(); len(pending) > 0 {
			logger.Debug("dispatcher: serving %d TUI pending permissions", len(pending))
			writeJSON(w, http.StatusOK, flattenEvents(pending))
			return
		}
	}
	s.handleProxy(w, r)
}

// handleQuestionList dispatches GET /questions. Same pattern as
// handlePermissionList but for question.asked entries.
//
// @Summary      List pending questions
// @Description  Dispatches by source: returns TUI-sourced pending questions when present, otherwise proxies to the agent backend.
// @Tags         Question
// @Produce      json
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      503  {object}  envelope
// @Router       /questions [get]
func (s *Server) handleQuestionList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use GET")
		return
	}
	if s.tuiRegistry != nil {
		if pending := s.tuiRegistry.PendingQuestions(); len(pending) > 0 {
			logger.Debug("dispatcher: serving %d TUI pending questions", len(pending))
			writeJSON(w, http.StatusOK, flattenEvents(pending))
			return
		}
	}
	s.handleProxy(w, r)
}

// flattenEvents converts TUI registry's []agent.Event (where the id lives
// inside Data: {type, data: {id, ...}}) into the bare-array shape SaaS's
// parseIDList decodes: [{id: "...", sessionID: "...", ...}, ...]. Each event's
// Data map is shallow-merged with a top-level "id" and "sessionID" so consumers
// reading either field work without drilling into the envelope. Unknown / non-map
// Data falls back to {id: "", sessionID: conversation_id}.
func flattenEvents(events []agent.Event) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, evt := range events {
		entry := map[string]any{}
		if data, ok := evt.Data.(map[string]any); ok {
			for k, v := range data {
				entry[k] = v
			}
		}
		if _, hasID := entry["id"]; !hasID {
			if qid, ok := entry["questionID"].(string); ok && qid != "" {
				entry["id"] = qid
			}
		}
		if _, hasSession := entry["sessionID"]; !hasSession && evt.ConversationID != "" {
			entry["sessionID"] = evt.ConversationID
		}
		out = append(out, entry)
	}
	return out
}
