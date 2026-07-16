package localserver

import (
	"encoding/json"
	"io"
	"net/http"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

// maxReplyBodyBytes caps a single POST body for /permissions/{id}/reply and
// the question sibling endpoints. Reply payloads are tiny ({decision, reason}
// or {text}); 64KB is a generous ceiling that still rejects pathological input.
const maxReplyBodyBytes = 64 * 1024

// replyPayload is the loose wire shape accepted by the POST reply endpoints.
// All fields are optional — the dispatcher forwards whatever the caller sent
// without imposing semantics. The cloud notifier (downstream of the EventBus)
// decides how to render the payload for WeChat Work.
type replyPayload struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Text     string `json:"text,omitempty"`
}

// handlePermissionReply dispatches POST /permissions/{id}/reply.
//
// Per design decision 3:
//   - id is TUI-sourced (csc TUI pushed the matching permission.asked via
//     POST /runtime/events): synthesize a `permission.replied` event with
//     the reply payload and Emit it on the EventBus, return 204 No Content.
//     handleProxy is NOT called — there is no csc-serve request to forward.
//     The cloud-side WeChat Work notifier consumes the emitted event via the
//     EventBus subscriber chain and forwards the reply onward.
//   - id is csc-serve-sourced or unknown: delegate to handleProxy unchanged.
//     Behaviour for the pre-Phase-2 endpoint stays byte-identical.
//
// After a successful TUI emit, the registry entry is forgotten so a duplicate
// reply falls through to handleProxy (which will 404 on the backend rather
// than silently re-emitting).
//
// @Summary      Reply to permission
// @Description  Dispatches by source: TUI-sourced ids emit a permission.replied event; csc-serve ids proxy to the agent backend.
// @Tags         Permission
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Permission ID"
// @Success      204  {string}  string  "No Content"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      400  {object}  envelope
// @Failure      503  {object}  envelope
// @Router       /permissions/{id}/reply [post]
func (s *Server) handlePermissionReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	id := r.PathValue("id")
	if s.tuiRegistry != nil && s.tuiRegistry.IsTUISourced(id) {
		s.emitTUIReply(w, r, "permission.replied", id, "permission")
		return
	}
	s.handleProxy(w, r)
}

// handleQuestionReply dispatches POST /questions/{id}/reply. Same pattern as
// handlePermissionReply but emits `question.replied`.
//
// @Summary      Reply to question
// @Description  Dispatches by source: TUI-sourced ids emit a question.replied event; csc-serve ids proxy to the agent backend.
// @Tags         Question
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Question ID"
// @Success      204  {string}  string  "No Content"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      400  {object}  envelope
// @Failure      503  {object}  envelope
// @Router       /questions/{id}/reply [post]
func (s *Server) handleQuestionReply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	id := r.PathValue("id")
	if s.tuiRegistry != nil && s.tuiRegistry.IsTUISourced(id) {
		s.emitTUIReply(w, r, "question.replied", id, "question")
		return
	}
	s.handleProxy(w, r)
}

// handleQuestionReject dispatches POST /questions/{id}/reject. Same pattern
// as handleQuestionReply but emits `question.rejected` — the cloud notifier
// treats this as a "user dismissed / declined" signal.
//
// @Summary      Reject question
// @Description  Dispatches by source: TUI-sourced ids emit a question.rejected event; csc-serve ids proxy to the agent backend.
// @Tags         Question
// @Accept       json
// @Produce      json
// @Param        id   path      string  true  "Question ID"
// @Success      204  {string}  string  "No Content"
// @Success      200  {object}  envelope{data=map[string]any}
// @Failure      400  {object}  envelope
// @Failure      503  {object}  envelope
// @Router       /questions/{id}/reject [post]
func (s *Server) handleQuestionReject(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "use POST")
		return
	}
	id := r.PathValue("id")
	if s.tuiRegistry != nil && s.tuiRegistry.IsTUISourced(id) {
		s.emitTUIReply(w, r, "question.rejected", id, "question")
		return
	}
	s.handleProxy(w, r)
}

// emitTUIReply reads the reply body, emits an event of the given type
// carrying the parsed payload, then returns 204 No Content. On success the
// registry entry is dropped so a second reply falls through to handleProxy.
//
// The emitted event's Data carries both the parsed fields (decision/reason/
// text) and the raw JSON, so downstream consumers can pick whichever shape
// matches their notification template without round-tripping through the
// wire format again.
func (s *Server) emitTUIReply(w http.ResponseWriter, r *http.Request, eventType, id, kind string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxReplyBodyBytes))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "failed to read body: "+err.Error())
		return
	}

	var payload replyPayload
	data := map[string]any{
		"id":   id,
		"kind": kind,
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &payload); err != nil {
			writeErr(w, http.StatusBadRequest, "BAD_JSON", "invalid JSON: "+err.Error())
			return
		}
		// Preserve the original JSON object so consumers can access fields
		// outside decision/reason/text without changing this dispatcher.
		var raw any
		if json.Unmarshal(body, &raw) == nil {
			data["raw"] = raw
		}
	}
	if payload.Decision != "" {
		data["decision"] = payload.Decision
	}
	if payload.Reason != "" {
		data["reason"] = payload.Reason
	}
	if payload.Text != "" {
		data["text"] = payload.Text
	}

	conv := ""
	if s.tuiRegistry != nil {
		conv = s.tuiRegistry.ConversationOf(id)
	}

	evt := agent.Event{
		Type:           eventType,
		ConversationID: conv,
		Data:           data,
	}
	s.eventBus.Emit(evt)
	// Drop the registry entry so a duplicate reply falls through to the
	// proxy (which will 404) rather than re-emitting and double-notifying.
	if s.tuiRegistry != nil {
		s.tuiRegistry.Forget(id)
	}
	logger.Debug("dispatcher: emitted %s id=%s conv=%s source=tui", eventType, id, conv)

	w.WriteHeader(http.StatusNoContent)
}
