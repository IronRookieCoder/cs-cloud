package localserver

import (
	"testing"
	"time"

	"cs-cloud/internal/agent"
)

// helper: build a fake event for tests
func fakeEvent(eventType, convID string) agent.Event {
	return agent.Event{
		Type:           eventType,
		ConversationID: convID,
		Data:           map[string]any{"id": "x"},
	}
}

func TestRingBufferAppendAndSize(t *testing.T) {
	rb := NewRingBuffer(nil) // no bus needed for direct Append tests
	rb.Append(fakeEvent("permission.asked", "sess-a"))
	rb.Append(fakeEvent("question.asked", "sess-a"))
	if got := rb.Size(); got != 2 {
		t.Fatalf("Size: want 2, got %d", got)
	}
}

func TestRingBufferQueryAll(t *testing.T) {
	rb := NewRingBuffer(nil)
	rb.Append(fakeEvent("permission.asked", "sess-a"))
	rb.Append(fakeEvent("session.idle", "sess-b"))

	out := rb.Query(0, "")
	if len(out) != 2 {
		t.Fatalf("Query: want 2 events, got %d", len(out))
	}
}

func TestRingBufferQueryByConversation(t *testing.T) {
	rb := NewRingBuffer(nil)
	rb.Append(fakeEvent("permission.asked", "sess-a"))
	rb.Append(fakeEvent("session.idle", "sess-b"))
	rb.Append(fakeEvent("question.asked", "sess-a"))

	out := rb.Query(0, "sess-a")
	if len(out) != 2 {
		t.Fatalf("Query conv=sess-a: want 2, got %d", len(out))
	}
	for _, e := range out {
		if e.ConversationID != "sess-a" {
			t.Errorf("expected sess-a, got %s", e.ConversationID)
		}
	}
}

func TestRingBufferQuerySinceFilter(t *testing.T) {
	rb := NewRingBuffer(nil)
	rb.Append(fakeEvent("permission.asked", "sess-a"))

	// future cutoff returns nothing
	future := time.Now().Add(10 * time.Second).UnixMilli()
	out := rb.Query(future, "")
	if len(out) != 0 {
		t.Fatalf("Query future: want 0, got %d", len(out))
	}

	// past cutoff returns the event
	past := time.Now().Add(-1 * time.Minute).UnixMilli()
	out = rb.Query(past, "")
	if len(out) != 1 {
		t.Fatalf("Query past: want 1, got %d", len(out))
	}
}

func TestRingBufferQueryEmpty(t *testing.T) {
	rb := NewRingBuffer(nil)
	out := rb.Query(0, "")
	if len(out) != 0 {
		t.Fatalf("Query empty buffer: want 0, got %d", len(out))
	}
}

func TestRingBufferCapacityOverflow(t *testing.T) {
	rb := NewRingBuffer(nil)
	rb.cap = 4 // shrink for test
	rb.entries = make([]*bufferedEvent, rb.cap)

	for i := 0; i < 10; i++ {
		rb.Append(fakeEvent("permission.asked", "sess-a"))
	}
	if rb.Size() != rb.cap {
		t.Fatalf("Size after overflow: want %d (cap), got %d", rb.cap, rb.Size())
	}
}

func TestRingBufferTTLEviction(t *testing.T) {
	rb := NewRingBuffer(nil)
	rb.ttl = 50 * time.Millisecond

	rb.Append(fakeEvent("permission.asked", "sess-a"))
	time.Sleep(80 * time.Millisecond)
	rb.Append(fakeEvent("session.idle", "sess-a"))

	// Query triggers prune; first event should be evicted.
	out := rb.Query(0, "")
	if len(out) != 1 {
		t.Fatalf("After TTL prune: want 1 event, got %d", len(out))
	}
	if out[0].Type != "session.idle" {
		t.Errorf("expected session.idle, got %s", out[0].Type)
	}
}

func TestRingBufferConcurrency(t *testing.T) {
	rb := NewRingBuffer(nil)
	done := make(chan struct{})
	for i := 0; i < 100; i++ {
		go func(n int) {
			rb.Append(fakeEvent("permission.asked", "sess-a"))
			_ = rb.Size()
			_ = rb.Query(0, "")
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 100; i++ {
		<-done
	}
	if rb.Size() > 100 {
		t.Fatalf("Size > 100 after concurrent appends: %d", rb.Size())
	}
}

func TestRingBufferOrder(t *testing.T) {
	rb := NewRingBuffer(nil)
	rb.Append(agent.Event{Type: "first", ConversationID: "s"})
	rb.Append(agent.Event{Type: "second", ConversationID: "s"})
	rb.Append(agent.Event{Type: "third", ConversationID: "s"})

	out := rb.Query(0, "s")
	if len(out) != 3 {
		t.Fatalf("want 3, got %d", len(out))
	}
	if out[0].Type != "first" || out[1].Type != "second" || out[2].Type != "third" {
		t.Errorf("order: %v,%v,%v", out[0].Type, out[1].Type, out[2].Type)
	}
}
