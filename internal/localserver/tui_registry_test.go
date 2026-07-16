package localserver

import (
	"context"
	"testing"
	"time"

	"cs-cloud/internal/agent"
)

// permEvt / questionEvt builders produce the shape csc TUI pushes via
// POST /runtime/events. data.id / data.questionID is what extractEventID
// keys on.
func permEvt(id, conv string) agent.Event {
	return agent.Event{
		Type:           "permission.asked",
		ConversationID: conv,
		Data:           map[string]any{"id": id},
	}
}

func questionEvt(id, conv string) agent.Event {
	return agent.Event{
		Type:           "question.asked",
		ConversationID: conv,
		Data:           map[string]any{"questionID": id},
	}
}

func TestTUIRegistryRegisterAndLookup(t *testing.T) {
	r := NewTUIRegistry()
	if err := r.Register(permEvt("perm-1", "conv-a")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !r.IsTUISourced("perm-1") {
		t.Errorf("IsTUISourced(perm-1): want true")
	}
	if got := r.ConversationOf("perm-1"); got != "conv-a" {
		t.Errorf("ConversationOf: want conv-a, got %q", got)
	}
}

func TestTUIRegistryUnknownIDNotSourced(t *testing.T) {
	r := NewTUIRegistry()
	r.Register(permEvt("perm-1", "conv-a"))
	if r.IsTUISourced("does-not-exist") {
		t.Errorf("IsTUISourced(unknown): want false")
	}
	if got := r.ConversationOf("does-not-exist"); got != "" {
		t.Errorf("ConversationOf(unknown): want empty, got %q", got)
	}
}

func TestTUIRegistryEmptyIDsNotSourced(t *testing.T) {
	r := NewTUIRegistry()
	if r.IsTUISourced("") {
		t.Errorf("IsTUISourced(\"\"): want false")
	}
	if got := r.ConversationOf(""); got != "" {
		t.Errorf("ConversationOf(\"\"): want empty, got %q", got)
	}
}

func TestTUIRegistryDualOwnershipRejected(t *testing.T) {
	r := NewTUIRegistry()
	if err := r.Register(permEvt("perm-1", "conv-a")); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(permEvt("perm-1", "conv-b"))
	if err != ErrDualOwnership {
		t.Errorf("second Register: want ErrDualOwnership, got %v", err)
	}
}

func TestTUIRegistryNonTrackingEventsAccepted(t *testing.T) {
	// Events without an id-bearing Data (session.idle, etc.) have nothing
	// to track; Register must accept them as no-ops and return nil.
	r := NewTUIRegistry()
	if err := r.Register(agent.Event{Type: "session.idle", ConversationID: "conv-a"}); err != nil {
		t.Fatalf("Register(session.idle): %v", err)
	}
	if r.Size() != 0 {
		t.Errorf("Size after non-tracking event: want 0, got %d", r.Size())
	}
}

func TestTUIRegistryPendingFiltersByType(t *testing.T) {
	r := NewTUIRegistry()
	r.Register(permEvt("perm-1", "conv-a"))
	r.Register(permEvt("perm-2", "conv-a"))
	r.Register(questionEvt("q-1", "conv-a"))
	r.Register(questionEvt("q-2", "conv-b"))

	if got := len(r.PendingPermissions()); got != 2 {
		t.Errorf("PendingPermissions: want 2, got %d", got)
	}
	if got := len(r.PendingQuestions()); got != 2 {
		t.Errorf("PendingQuestions: want 2, got %d", got)
	}
}

func TestTUIRegistryForgetRemovesEntry(t *testing.T) {
	r := NewTUIRegistry()
	r.Register(permEvt("perm-1", "conv-a"))
	if !r.IsTUISourced("perm-1") {
		t.Fatalf("pre-Forget: want sourced")
	}
	r.Forget("perm-1")
	if r.IsTUISourced("perm-1") {
		t.Errorf("post-Forget: want not sourced")
	}
	// Forget is idempotent — calling again on a forgotten id is a no-op.
	r.Forget("perm-1")
	r.Forget("")
}

func TestTUIRegistryExpiry(t *testing.T) {
	r := NewTUIRegistry()
	// Override TTL to a tiny value so we can observe expiry without flakiness.
	r.ttl = 20 * time.Millisecond
	r.Register(permEvt("perm-1", "conv-a"))

	// Sanity: sourced immediately after register.
	if !r.IsTUISourced("perm-1") {
		t.Fatalf("immediate: want sourced")
	}

	// Wait past TTL.
	time.Sleep(40 * time.Millisecond)

	if r.IsTUISourced("perm-1") {
		t.Errorf("post-TTL: want not sourced")
	}
	if got := r.ConversationOf("perm-1"); got != "" {
		t.Errorf("post-TTL ConversationOf: want empty, got %q", got)
	}
	if got := len(r.PendingPermissions()); got != 0 {
		t.Errorf("post-TTL PendingPermissions: want 0, got %d", got)
	}
}

func TestTUIRegistryCleanupRemovesExpired(t *testing.T) {
	r := NewTUIRegistry()
	r.ttl = 20 * time.Millisecond
	r.Register(permEvt("perm-1", "conv-a"))
	r.Register(permEvt("perm-2", "conv-a"))
	if r.Size() != 2 {
		t.Fatalf("pre-expiry Size: want 2, got %d", r.Size())
	}

	time.Sleep(40 * time.Millisecond)
	removed := r.Cleanup()
	if removed != 2 {
		t.Errorf("Cleanup removed: want 2, got %d", removed)
	}
	if r.Size() != 0 {
		t.Errorf("post-Cleanup Size: want 0, got %d", r.Size())
	}
}

// TestTUIRegistryExpiredIDCanBeReRegistered guards against the verifier-found
// bug where Register rejected any id present in the map regardless of expiry.
// The cleanup goroutine runs on a 1-min ticker, so between TTL elapse and
// the next Cleanup pass there is a window where an expired entry still holds
// a map slot — re-registration in that window MUST succeed (the entry is
// semantically dead). The 409 is reserved for genuine concurrent double-
// registration of a LIVE entry.
func TestTUIRegistryExpiredIDCanBeReRegistered(t *testing.T) {
	r := NewTUIRegistry()
	r.ttl = 20 * time.Millisecond
	if err := r.Register(permEvt("perm-1", "conv-a")); err != nil {
		t.Fatalf("first register: %v", err)
	}

	time.Sleep(40 * time.Millisecond) // past TTL, but BEFORE the 1-min Cleanup tick

	// Same id, different conversation — must succeed because the prior entry
	// is expired. Without the expiry check in Register this returned 409.
	if err := r.Register(permEvt("perm-1", "conv-b")); err != nil {
		t.Fatalf("re-register expired id: want nil, got %v", err)
	}

	// The new entry must be live and reflect the new conversation.
	if !r.IsTUISourced("perm-1") {
		t.Errorf("post re-register: want sourced")
	}
	if got := r.ConversationOf("perm-1"); got != "conv-b" {
		t.Errorf("ConversationOf: want conv-b (overwritten), got %q", got)
	}
}

func TestTUIRegistryStartStopLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	r := NewTUIRegistry()
	r.Start(ctx)
	t.Cleanup(r.Stop)

	// Double Start is a no-op.
	r.Start(ctx)

	// Stop is idempotent.
	r.Stop()
	r.Stop()
}

func TestShouldRegisterTUI(t *testing.T) {
	cases := map[string]bool{
		"permission.asked":   true,
		"question.asked":     true,
		"session.idle":       false,
		"permission.replied": false,
		"question.replied":   false,
		"":                   false,
	}
	for eventType, want := range cases {
		if got := shouldRegisterTUI(eventType); got != want {
			t.Errorf("shouldRegisterTUI(%q): want %v, got %v", eventType, want, got)
		}
	}
}
