package main

import (
	"sync"
	"time"
)

// Event is one line in the live activity feed pushed to the UI over SSE.
type Event struct {
	Type    string    `json:"type"` // connected | disconnected | stream | ping | kick
	AgentID string    `json:"agent_id"`
	Message string    `json:"message"`
	At      time.Time `json:"at"`
}

const (
	eventBacklog = 100
	subBuffer    = 64
)

// EventBus fans events out to every connected SSE client and keeps a short
// backlog so a browser that opens the page late still sees recent history.
type EventBus struct {
	mu      sync.Mutex
	subs    map[chan Event]struct{}
	backlog []Event
}

func newEventBus() *EventBus {
	return &EventBus{subs: make(map[chan Event]struct{})}
}

func (b *EventBus) Publish(e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.backlog = append(b.backlog, e)
	if len(b.backlog) > eventBacklog {
		b.backlog = b.backlog[len(b.backlog)-eventBacklog:]
	}
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // slow client: drop rather than stall the server
		}
	}
}

// Subscribe returns the current backlog plus a channel of future events. The
// returned func must be called to release the subscription.
func (b *EventBus) Subscribe() ([]Event, <-chan Event, func()) {
	ch := make(chan Event, subBuffer)

	b.mu.Lock()
	history := append([]Event(nil), b.backlog...)
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	return history, ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}
