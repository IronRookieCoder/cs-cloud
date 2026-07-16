package localserver

import (
	"testing"

	"cs-cloud/internal/agent"
)

// Phase 1 stub tests: all methods are no-ops, but the signatures must be
// stable so Phase 2 can replace internals without touching callers.

func TestTUIRegistryStubRegisterReturnsNil(t *testing.T) {
	r := NewTUIRegistry()
	if err := r.Register(agent.Event{Type: "permission.asked"}); err != nil {
		t.Fatalf("Phase 1 Register must return nil, got %v", err)
	}
}

func TestTUIRegistryStubIsTUISourcedFalse(t *testing.T) {
	r := NewTUIRegistry()
	r.Register(agent.Event{Type: "permission.asked"})
	if r.IsTUISourced("anything") {
		t.Errorf("Phase 1 IsTUISourced must always be false")
	}
}

func TestTUIRegistryStubPendingPermissionsEmpty(t *testing.T) {
	r := NewTUIRegistry()
	r.Register(agent.Event{Type: "permission.asked"})
	if got := r.PendingPermissions(); len(got) != 0 {
		t.Errorf("Phase 1 PendingPermissions must be empty, got %d", len(got))
	}
}

func TestTUIRegistryStubPendingQuestionsEmpty(t *testing.T) {
	r := NewTUIRegistry()
	r.Register(agent.Event{Type: "question.asked"})
	if got := r.PendingQuestions(); len(got) != 0 {
		t.Errorf("Phase 1 PendingQuestions must be empty, got %d", len(got))
	}
}

func TestShouldRegisterTUI(t *testing.T) {
	cases := map[string]bool{
		"permission.asked":    true,
		"question.asked":      true,
		"session.idle":        false,
		"permission.replied":  false,
		"question.replied":    false,
		"":                    false,
	}
	for eventType, want := range cases {
		if got := shouldRegisterTUI(eventType); got != want {
			t.Errorf("shouldRegisterTUI(%q): want %v, got %v", eventType, want, got)
		}
	}
}
