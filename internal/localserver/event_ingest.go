package localserver

import (
	"encoding/json"
	"io"
	"net/http"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

// maxEventBodyBytes caps a single POST /runtime/events body. Events are
// small metadata envelopes (permission/question/idle), but a sane ceiling
// protects the daemon from pathological inputs.
const maxEventBodyBytes = 64 * 1024

// ingressEvent mirrors agent.Event but accepts both snake_case and
// camelCase for ConversationID. The canonical wire format is snake_case
// (`conversation_id`); camelCase is tolerated because the csc TUI client
// is TypeScript and may default to camelCase. Data flows through as-is.
type ingressEvent struct {
	Type           string `json:"type"`
	ConversationID string `json:"conversation_id"`
	ConvIDCamel    string `json:"conversationID,omitempty"`
	MessageID      string `json:"msg_id,omitempty"`
	Backend        string `json:"backend,omitempty"`
	Data           any    `json:"data"`
}

func (e *ingressEvent) toAgentEvent() agent.Event {
	out := agent.Event{
		Type:           e.Type,
		ConversationID: e.ConversationID,
		MessageID:      e.MessageID,
		Backend:        e.Backend,
		Data:           e.Data,
	}
	if out.ConversationID == "" {
		out.ConversationID = e.ConvIDCamel
	}
	normalizeEventData(&out)
	return out
}

// normalizeEventData aligns csc TUI's native event payload shape with the
// shape SaaS already consumes (csc-serve CloudConnector historical contract).
// Today only question.asked needs adjustment: csc emits questionID as the
// canonical device-side identifier (matches Claude tool_use IDs), while SaaS's
// permission_manager / question_manager / parseIDList read data.id. Copying
// questionID → id at ingress means every downstream consumer (tui_registry,
// NotifyForwarder, EventBus subscribers, GET /questions handlers) sees a
// uniform shape and no SaaS code needs to learn a second field name.
//
// The original questionID field is preserved so anything reading it (e.g.
// tui_registry.extractEventID's question.asked branch) keeps working unchanged.
func normalizeEventData(evt *agent.Event) {
	if evt == nil || evt.Data == nil {
		return
	}
	if evt.Type != "question.asked" {
		return
	}
	m, ok := evt.Data.(map[string]any)
	if !ok || m == nil {
		return
	}
	if _, hasID := m["id"]; hasID {
		return
	}
	if qid, ok := m["questionID"].(string); ok && qid != "" {
		m["id"] = qid
	}
}

// handleRuntimeEventPost accepts events pushed by csc TUI (and any future
// local client) into the cs-cloud EventBus. The handler is intentionally
// permissive: any event type is accepted, downstream consumers (NotifyForwarder,
// ring buffer) decide what to do with it.
//
// Phase 1: no TUI registry; permission/question ownership tracking arrives in Phase 2.
func (s *Server) handleRuntimeEventPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body: "+err.Error())
		return
	}

	var ingress ingressEvent
	if err := json.Unmarshal(body, &ingress); err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_JSON", "invalid JSON: "+err.Error())
		return
	}
	if ingress.Type == "" {
		writeErr(w, http.StatusBadRequest, "MISSING_TYPE", `"type" is required`)
		return
	}

	evt := ingress.toAgentEvent()

	// Phase 1 stub: TUI registry is a no-op; Phase 2 will record ownership
	// for permission.asked / question.asked and return 409 on dual-ownership conflicts.
	if s.tuiRegistry != nil {
		if err := s.tuiRegistry.Register(evt); err != nil {
			logger.Warn("runtime events: tui registry rejected event type=%s: %v", evt.Type, err)
			writeErr(w, http.StatusConflict, "DUAL_OWNERSHIP", err.Error())
			return
		}
	}

	s.eventBus.Emit(evt)
	logger.Debug("runtime events: ingested type=%s conv=%s source=tui", evt.Type, evt.ConversationID)

	w.WriteHeader(http.StatusNoContent)
}
