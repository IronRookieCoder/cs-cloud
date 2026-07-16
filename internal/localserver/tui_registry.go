package localserver

import (
	"context"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
)

const defaultRegistryTTL = 10 * time.Minute

// ErrDualOwnership is returned by Register when the event's id is already
// known to belong to a different source (e.g. csc serve reported it via SSE).
var ErrDualOwnership = constErr("dual-ownership conflict")

type constErr string

func (e constErr) Error() string { return string(e) }

// TUIRegistry records which permissionIDs/questionIDs were pushed by the
// csc TUI (via POST /api/v1/runtime/events) so the reply/query dispatchers
// can route responses back through the EventBus instead of handleProxy.
//
// Lifecycle: NewTUIRegistry returns a fresh instance; Start launches a
// background cleanup goroutine that prunes expired entries every minute;
// Stop cancels it. The registry is safe for concurrent use.
type TUIRegistry struct {
	mu   sync.RWMutex
	ids  map[string]*registryEntry
	ttl  time.Duration

	cancel context.CancelFunc
	done   chan struct{}
}

type registryEntry struct {
	event         agent.Event
	id            string
	eventType     string    // "permission.asked" / "question.asked"
	conversationID string
	workspace     string
	pushedAt      time.Time
	expiresAt     time.Time
}

// NewTUIRegistry returns a registry with the default 10min TTL
// (aligned with csc TUI's pollTimeoutMin to avoid mid-window expiry).
func NewTUIRegistry() *TUIRegistry {
	return &TUIRegistry{
		ids: make(map[string]*registryEntry),
		ttl: defaultRegistryTTL,
	}
}

// Start launches the background cleanup goroutine. Safe to call once;
// subsequent calls are no-ops. The goroutine exits when Stop is called
// or ctx is cancelled.
func (r *TUIRegistry) Start(ctx context.Context) {
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	r.done = make(chan struct{})

	go func() {
		defer close(r.done)
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.Cleanup()
			}
		}
	}()
}

// Stop signals the cleanup goroutine to exit and blocks until it has.
// Safe to call multiple times.
func (r *TUIRegistry) Stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	r.cancel = nil
	if r.done != nil {
		<-r.done
		r.done = nil
	}
}

// Register records evt as TUI-sourced. The id is extracted from Data based
// on Type: permission.asked → data.id, question.asked → data.questionID.
// Returns ErrDualOwnership only if the id is held by a LIVE (un-expired)
// entry. An expired-but-unpruned entry is treated as absent: the stale slot
// is overwritten in place. This matters because the cleanup goroutine runs
// on a 1-min ticker, so there is a window where a TTL'd entry still holds
// a map slot — refusing re-registration with a spurious 409 in that window
// would block legitimate retries from csc TUI.
func (r *TUIRegistry) Register(evt agent.Event) error {
	id := extractEventID(evt)
	if id == "" {
		// Non-permission/question events have no id to track; accept silently.
		return nil
	}

	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, exists := r.ids[id]; exists && now.Before(existing.expiresAt) {
		return ErrDualOwnership
	}

	r.ids[id] = &registryEntry{
		event:          evt,
		id:             id,
		eventType:      evt.Type,
		conversationID: evt.ConversationID,
		pushedAt:       now,
		expiresAt:      now.Add(r.ttl),
	}
	return nil
}

// IsTUISourced reports whether id was TUI-pushed and has not yet expired.
func (r *TUIRegistry) IsTUISourced(id string) bool {
	if id == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.ids[id]
	if !ok {
		return false
	}
	return time.Now().Before(entry.expiresAt)
}

// ConversationOf returns the conversationID the TUI-sourced id was registered
// under (""), or "" if the id is unknown or expired. Used by the reply
// dispatcher to populate ConversationID on the emitted *.replied event so
// downstream consumers (cloud notifier) can route the reply correctly.
func (r *TUIRegistry) ConversationOf(id string) string {
	if id == "" {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.ids[id]
	if !ok || !time.Now().Before(entry.expiresAt) {
		return ""
	}
	return entry.conversationID
}

// Forget removes the entry for id, if any. Called by reply dispatchers after
// emitting the *.replied event so a stale pending entry can't be replied to
// twice (the second reply would fall through to handleProxy and 404 on the
// backend, which is correct but noisy).
func (r *TUIRegistry) Forget(id string) {
	if id == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.ids, id)
}

// PendingPermissions returns all live permission.asked entries.
// Used by the GET /permissions dispatcher to merge with the proxy response.
func (r *TUIRegistry) PendingPermissions() []agent.Event {
	return r.pendingByType("permission.asked")
}

// PendingQuestions returns all live question.asked entries.
func (r *TUIRegistry) PendingQuestions() []agent.Event {
	return r.pendingByType("question.asked")
}

func (r *TUIRegistry) pendingByType(wantType string) []agent.Event {
	now := time.Now()
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]agent.Event, 0, len(r.ids))
	for _, entry := range r.ids {
		if entry.eventType != wantType {
			continue
		}
		if !now.Before(entry.expiresAt) {
			continue
		}
		out = append(out, entry.event)
	}
	return out
}

// Cleanup removes expired entries. Called automatically by the background
// goroutine; exposed for tests.
func (r *TUIRegistry) Cleanup() int {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	removed := 0
	for id, entry := range r.ids {
		if !now.Before(entry.expiresAt) {
			delete(r.ids, id)
			removed++
		}
	}
	if removed > 0 {
		logger.Debug("tui registry cleanup: removed=%d remaining=%d", removed, len(r.ids))
	}
	return removed
}

// Size returns the current entry count. For tests/diagnostics.
func (r *TUIRegistry) Size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.ids)
}

// extractEventID pulls the unique ID from an event's Data field based on Type.
// permission.asked → data.id; question.asked → data.questionID.
// Returns "" for events without a trackable ID (or malformed Data).
func extractEventID(evt agent.Event) string {
	m, ok := evt.Data.(map[string]any)
	if !ok {
		return ""
	}
	switch evt.Type {
	case "permission.asked":
		if v, ok := m["id"].(string); ok {
			return v
		}
	case "question.asked":
		if v, ok := m["questionID"].(string); ok {
			return v
		}
	}
	return ""
}

// shouldRegisterTUI reports whether an event type should be recorded in
// the TUI registry. Only ingress permission/question types belong here.
func shouldRegisterTUI(eventType string) bool {
	switch eventType {
	case "permission.asked", "question.asked":
		return true
	}
	return false
}
