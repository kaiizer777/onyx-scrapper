package teacher

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// StreamEvent represents a single event in the Teacher Agent SSE event stream.
type StreamEvent struct {
	RunID     string      `json:"run_id"`
	Event     string      `json:"event"`
	Data      interface{} `json:"data,omitempty"`
	Timestamp string      `json:"timestamp"`
}

// FormatSSE formats the stream event into standard Server-Sent Event data framing.
func (e StreamEvent) FormatSSE() string {
	b, _ := json.Marshal(e)
	return fmt.Sprintf("data: %s\n\n", string(b))
}

// EventBroadcaster manages thread-safe Server-Sent Event subscriptions and broadcasts for active runs.
type EventBroadcaster struct {
	mu          sync.RWMutex
	subscribers map[string]map[uint64]chan StreamEvent
	closedSubs  map[uint64]bool
	nextSubID   uint64
}

// NewEventBroadcaster constructs a new EventBroadcaster instance.
func NewEventBroadcaster() *EventBroadcaster {
	return &EventBroadcaster{
		subscribers: make(map[string]map[uint64]chan StreamEvent),
		closedSubs:  make(map[uint64]bool),
	}
}

// Subscribe registers a new subscriber channel for a given runID.
// It returns a read-only channel receiving events and an unsubscribe cleanup function.
func (b *EventBroadcaster) Subscribe(runID string) (<-chan StreamEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextSubID++
	subID := b.nextSubID

	ch := make(chan StreamEvent, 64)
	if _, ok := b.subscribers[runID]; !ok {
		b.subscribers[runID] = make(map[uint64]chan StreamEvent)
	}
	b.subscribers[runID][subID] = ch

	var once sync.Once
	unsubscribe := func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()

			if b.closedSubs[subID] {
				delete(b.closedSubs, subID)
				return
			}
			delete(b.closedSubs, subID)

			if subs, ok := b.subscribers[runID]; ok {
				if subCh, found := subs[subID]; found {
					delete(subs, subID)
					close(subCh)
				}
				if len(subs) == 0 {
					delete(b.subscribers, runID)
				}
			}
		})
	}

	return ch, unsubscribe
}

// Broadcast dispatches a stream event to all active subscribers for the event's runID.
func (b *EventBroadcaster) Broadcast(event StreamEvent) {
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339)
	}

	b.mu.RLock()
	defer b.mu.RUnlock()

	subsMap, exists := b.subscribers[event.RunID]
	if !exists || len(subsMap) == 0 {
		return
	}

	for subID, ch := range subsMap {
		if b.closedSubs[subID] {
			continue
		}
		select {
		case ch <- event:
		default:
			// Non-blocking drop if consumer buffer is saturated
		}
	}
}

// CloseRun terminates all active subscriber channels for a given runID.
func (b *EventBroadcaster) CloseRun(runID string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if subs, ok := b.subscribers[runID]; ok {
		for subID, ch := range subs {
			if !b.closedSubs[subID] {
				b.closedSubs[subID] = true
				close(ch)
			}
		}
		delete(b.subscribers, runID)
	}
}
