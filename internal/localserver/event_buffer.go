package localserver

import (
	"context"
	"sync"
	"time"

	"cs-cloud/internal/agent"
	"cs-cloud/internal/logger"
	"cs-cloud/internal/runtime"
)

const (
	defaultRingBufferCapacity = 10000
	defaultRingBufferTTL      = 5 * time.Minute
)

// RingBuffer subscribes to the runtime.EventBus and retains recent events
// in-memory for GET /api/v1/runtime/events polling. Events older than the
// TTL or exceeding the capacity ceiling are evicted automatically.
type RingBuffer struct {
	mu      sync.RWMutex
	entries []*bufferedEvent
	head    int // ring write cursor
	size    int // current entry count (<= capacity)
	cap     int
	ttl     time.Duration

	bus     *runtime.EventBus
	cancel  context.CancelFunc
	doneCh  chan struct{}
}

type bufferedEvent struct {
	event agent.Event
	ts    time.Time
}

// NewRingBuffer constructs a RingBuffer with default capacity and TTL,
// bound to the given EventBus. Call Start to begin draining events;
// call Stop to release the subscription on shutdown.
func NewRingBuffer(bus *runtime.EventBus) *RingBuffer {
	return &RingBuffer{
		entries: make([]*bufferedEvent, defaultRingBufferCapacity),
		cap:     defaultRingBufferCapacity,
		ttl:     defaultRingBufferTTL,
		bus:     bus,
	}
}

// Start subscribes to the EventBus (no backend filter) and spawns a goroutine
// that drains the subscription channel into the buffer. The goroutine exits
// when Stop is called or the parent context is cancelled.
func (rb *RingBuffer) Start(ctx context.Context) {
	if rb.bus == nil {
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	rb.cancel = cancel
	rb.doneCh = make(chan struct{})

	ch := rb.bus.Subscribe(nil)
	go func() {
		defer close(rb.doneCh)
		defer rb.bus.Unsubscribe(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				rb.Append(evt)
			}
		}
	}()
}

// Stop signals the drain goroutine to exit and blocks until it has
// released the EventBus subscription. Safe to call multiple times.
func (rb *RingBuffer) Stop() {
	if rb.cancel == nil {
		return
	}
	rb.cancel()
	rb.cancel = nil
	if rb.doneCh != nil {
		<-rb.doneCh
		rb.doneCh = nil
	}
}

// Append adds an event to the buffer. Timestamped at wall-clock now; the
// oldest entry is overwritten once capacity is reached.
func (rb *RingBuffer) Append(evt agent.Event) {
	now := time.Now()
	rb.mu.Lock()
	defer rb.mu.Unlock()

	slot := rb.entries[rb.head]
	if slot == nil {
		slot = &bufferedEvent{}
		rb.entries[rb.head] = slot
	}
	slot.event = evt
	slot.ts = now
	rb.head = (rb.head + 1) % rb.cap
	if rb.size < rb.cap {
		rb.size++
	}
}

// Query returns events with timestamp >= sinceMs (Unix milliseconds),
// optionally filtered by conversationID. An empty conversationID returns
// events across all sessions. Expired entries are pruned before scanning.
func (rb *RingBuffer) Query(sinceMs int64, conversationID string) []agent.Event {
	now := time.Now()

	rb.mu.Lock()
	rb.pruneLocked(now)
	rb.mu.Unlock()

	rb.mu.RLock()
	defer rb.mu.RUnlock()

	out := make([]agent.Event, 0, rb.size)
	if rb.size == 0 {
		return out
	}
	cutoff := time.UnixMilli(sinceMs)
	start := (rb.head - rb.size + rb.cap) % rb.cap
	for i := 0; i < rb.size; i++ {
		idx := (start + i) % rb.cap
		slot := rb.entries[idx]
		if slot == nil {
			continue
		}
		if slot.ts.Before(cutoff) {
			continue
		}
		if conversationID != "" && slot.event.ConversationID != conversationID {
			continue
		}
		out = append(out, slot.event)
	}
	return out
}

// Size returns the current number of buffered events (post-prune).
func (rb *RingBuffer) Size() int {
	rb.mu.RLock()
	defer rb.mu.RUnlock()
	return rb.size
}

// pruneLocked removes expired entries. Caller must hold rb.mu.
// Live entries are compacted to the front of the ring so subsequent
// queries walk a contiguous logical window.
func (rb *RingBuffer) pruneLocked(now time.Time) {
	if rb.size == 0 {
		return
	}
	deadline := now.Add(-rb.ttl)
	start := (rb.head - rb.size + rb.cap) % rb.cap

	live := make([]*bufferedEvent, 0, rb.size)
	for i := 0; i < rb.size; i++ {
		idx := (start + i) % rb.cap
		slot := rb.entries[idx]
		if slot == nil {
			continue
		}
		if slot.ts.Before(deadline) {
			continue
		}
		live = append(live, slot)
	}

	for i := range rb.entries {
		rb.entries[i] = nil
	}
	for i, slot := range live {
		rb.entries[i] = slot
	}
	rb.head = len(live) % rb.cap
	rb.size = len(live)
	logger.Debug("ring buffer pruned: kept=%d cap=%d", len(live), rb.cap)
}
