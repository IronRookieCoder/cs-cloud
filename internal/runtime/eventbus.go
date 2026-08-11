package runtime

import (
	"sync"

	"cs-cloud/internal/agent"
)

type SubFilter struct {
	Backend string
	// ConversationID, when set, restricts delivery to events emitted for that
	// conversation/session id. Empty delivers all (subject to Backend).
	ConversationID string
}

type EventBus struct {
	mu              sync.RWMutex
	subscribers     map[chan agent.Event]*SubFilter
	sessionCwds     map[string]string
	activeWorkspace string
}

func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[chan agent.Event]*SubFilter),
		sessionCwds: make(map[string]string),
	}
}

func (b *EventBus) Subscribe(filter *SubFilter) chan agent.Event {
	ch := make(chan agent.Event, 64)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[ch] = filter
	return ch
}

func (b *EventBus) Unsubscribe(ch chan agent.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subscribers, ch)
	close(ch)
}

func (b *EventBus) Emit(event agent.Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch, filter := range b.subscribers {
		if filter != nil {
			if filter.Backend != "" && event.Backend != filter.Backend {
				continue
			}
			if filter.ConversationID != "" && event.ConversationID != filter.ConversationID {
				continue
			}
		}
		select {
		case ch <- event:
		default:
		}
	}
}

// SubscribeSession returns a channel that receives only events emitted for the
// given conversation/session id. It is a convenience over Subscribe with a
// SubFilter, for callers (e.g. the workflow driver watching an adopted turn)
// that need one session's busy/idle/done transitions rather than the firehose.
func (b *EventBus) SubscribeSession(conversationID string) chan agent.Event {
	return b.Subscribe(&SubFilter{ConversationID: conversationID})
}

func (b *EventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

func (b *EventBus) RegisterSessionCwd(sessionID, cwd string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sessionCwds[sessionID] = cwd
}

func (b *EventBus) GetSessionCwd(sessionID string) string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.sessionCwds[sessionID]
}

func (b *EventBus) UnregisterSessionCwd(sessionID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.sessionCwds, sessionID)
}

func (b *EventBus) SetActiveWorkspace(cwd string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.activeWorkspace = cwd
}

func (b *EventBus) GetActiveWorkspace() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.activeWorkspace
}
