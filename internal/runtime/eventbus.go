package runtime

import (
	"sync"

	"cs-cloud/internal/agent"
)

type SubFilter struct {
	Backend string
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
		if filter != nil && filter.Backend != "" && event.Backend != filter.Backend {
			continue
		}
		select {
		case ch <- event:
		default:
		}
	}
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
