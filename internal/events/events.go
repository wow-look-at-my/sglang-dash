// Package events carries the dashboard's event log and its fan-out to live
// subscribers.
package events

import (
	"sync"
	"time"
)

// Kind is a dotted "namespace.action" string. The UI derives severity and colour from its halves.
type Kind string

const (
	KindRequestArrived   Kind = "request.arrived"
	KindRequestAdmitted  Kind = "request.admitted"
	KindRequestFirstTok  Kind = "request.first_token"
	KindRequestCompleted Kind = "request.completed"
	KindRequestFailed    Kind = "request.failed"
	KindRequestAborted   Kind = "request.aborted"

	KindCacheHit      Kind = "cache.hit"
	KindCacheMiss     Kind = "cache.miss"
	KindCacheInserted Kind = "cache.inserted"
	KindCacheEvicted  Kind = "cache.evicted"
	KindCacheFlushed  Kind = "cache.flushed"

	KindSchedStalled    Kind = "scheduler.stalled"
	KindSchedPreempted  Kind = "scheduler.preempted"
	KindSchedRecovered  Kind = "scheduler.recovered"
	KindMemoryPressure  Kind = "memory.pressure"
	KindUpstreamUp      Kind = "upstream.reachable"
	KindUpstreamDown    Kind = "upstream.unreachable"
	KindScrapeFailed    Kind = "upstream.scrape_failed"
	KindSlowDetected    Kind = "latency.slow_request"
	KindDashboardNotice Kind = "dashboard.notice"
)

// Event is a single row of the activity feed.
type Event struct {
	Seq     uint64         `json:"seq"`
	Time    int64          `json:"time"` // ms since epoch
	Kind    Kind           `json:"kind"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// Bus keeps a bounded history and fans new events out to subscribers.
type Bus struct {
	mu   sync.RWMutex
	ring []Event
	head int // next write slot
	n    int // events currently stored
	seq  uint64
	subs map[int]chan Event
	next int
}

// NewBus returns a bus retaining the newest `capacity` events.
func NewBus(capacity int) *Bus {
	if capacity < 1 {
		capacity = 1
	}
	return &Bus{ring: make([]Event, capacity), subs: map[int]chan Event{}}
}

// Publish stamps and stores a single event, then delivers it to every
// subscriber. A subscriber whose buffer is full misses the event and is told
// so through its drop counter: the dashboard must never block the proxy path.
func (b *Bus) Publish(kind Kind, msg string, fields map[string]any) Event {
	b.mu.Lock()
	b.seq++
	ev := Event{Seq: b.seq, Time: time.Now().UnixMilli(), Kind: kind, Message: msg, Fields: fields}
	b.ring[b.head] = ev
	b.head = (b.head + 1) % len(b.ring)
	if b.n < len(b.ring) {
		b.n++
	}
	subs := make([]chan Event, 0, len(b.subs))
	for _, ch := range b.subs {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
	return ev
}

// Recent returns up to `limit` events, oldest earliest.
func (b *Bus) Recent(limit int) []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit <= 0 || limit > b.n {
		limit = b.n
	}
	out := make([]Event, 0, limit)
	start := (b.head - b.n + len(b.ring)) % len(b.ring)
	for i := b.n - limit; i < b.n; i++ {
		out = append(out, b.ring[(start+i)%len(b.ring)])
	}
	return out
}

// Subscribe returns a channel of future events and the function that closes it.
func (b *Bus) Subscribe(buffer int) (<-chan Event, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	id := b.next
	b.next++
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}
