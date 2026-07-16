package localserver

import (
	"cs-cloud/internal/agent"
)

// ErrDualOwnership is returned by Register when the given event's id is
// already known to belong to a different source (e.g. csc serve). Phase 2
// populates this; Phase 1 stub never returns it.
var ErrDualOwnership = constErr("dual-ownership conflict")

type constErr string

func (e constErr) Error() string { return string(e) }

// TUIRegistry records which permissionIDs/questionIDs were pushed by the
// csc TUI (via POST /api/v1/runtime/events) so the reply/query dispatchers
// can route responses back through the EventBus instead of handleProxy.
//
// Phase 1 stub: every method is a no-op. Phase 2 replaces the internals
// with a TTL-bounded map; the public API MUST remain stable to avoid
// rewriting Phase 1 callers.
type TUIRegistry struct{}

// NewTUIRegistry returns a Phase 1 stub instance. Phase 2 will return
// a real registry wired to a cleanup goroutine.
func NewTUIRegistry() *TUIRegistry {
	return &TUIRegistry{}
}

// Register records the event as TUI-sourced. Phase 1: always succeeds
// (returns nil). Phase 2: returns ErrDualOwnership if the event's id
// (data.id for permission, data.questionID for question) is already
// registered with a different source.
func (r *TUIRegistry) Register(evt agent.Event) error {
	return nil
}

// IsTUISourced reports whether the given id belongs to a TUI-pushed event
// and has not yet expired. Phase 1: always false.
func (r *TUIRegistry) IsTUISourced(id string) bool {
	return false
}

// PendingPermissions returns all live TUI-sourced permission.asked events,
// for the GET /api/v1/permissions dispatcher to merge with the proxy
// response. Phase 1: always empty.
func (r *TUIRegistry) PendingPermissions() []agent.Event {
	return nil
}

// PendingQuestions returns all live TUI-sourced question.asked events,
// for the GET /api/v1/questions dispatcher. Phase 1: always empty.
func (r *TUIRegistry) PendingQuestions() []agent.Event {
	return nil
}

// shouldRegisterTUI reports whether an event type should be recorded in
// the TUI registry. Only ingress permission/question types belong here;
// replies (permission.replied etc.) and idle events are not registered.
func shouldRegisterTUI(eventType string) bool {
	switch eventType {
	case "permission.asked", "question.asked":
		return true
	}
	return false
}
