package crawl

import (
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ItemStatus represents the status of a URL in the crawl queue.
type ItemStatus string

const (
	StatusPending    ItemStatus = "pending"
	StatusProcessing ItemStatus = "processing"
	StatusDone       ItemStatus = "done"
	StatusFailed     ItemStatus = "failed"
)

// QueueItem represents a single URL task in the crawl queue.
type QueueItem struct {
	URL       string     `json:"url"`
	Depth     int        `json:"depth"`
	Status    ItemStatus `json:"status"`
	Error     string     `json:"error,omitempty"`
	AddedAt   time.Time  `json:"added_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// QueueStats holds summary counts for the queue.
type QueueStats struct {
	Total      int `json:"total"`
	Pending    int `json:"pending"`
	Processing int `json:"processing"`
	Done       int `json:"done"`
	Failed     int `json:"failed"`
}

// Queue provides a thread-safe URL crawl queue with state tracking and deduplication.
type Queue struct {
	mu         sync.Mutex
	items      []*QueueItem
	seen       map[string]bool
	urlToIndex map[string]int
}

// NewQueue initializes an empty crawl queue.
func NewQueue() *Queue {
	return &Queue{
		items:      make([]*QueueItem, 0),
		seen:       make(map[string]bool),
		urlToIndex: make(map[string]int),
	}
}

// NormalizeURL cleans and normalizes a URL for deduplication.
func NormalizeURL(targetURL string) string {
	u, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return strings.TrimSpace(targetURL)
	}

	u.Fragment = "" // Strip anchor #tags
	u.Host = strings.ToLower(u.Host)
	u.Scheme = strings.ToLower(u.Scheme)

	// Strip trailing slash unless root path
	if len(u.Path) > 1 && strings.HasSuffix(u.Path, "/") {
		u.Path = strings.TrimSuffix(u.Path, "/")
	}

	return u.String()
}

// Enqueue adds a single URL to the queue if it has not been seen before.
func (q *Queue) Enqueue(targetURL string, depth int) bool {
	norm := NormalizeURL(targetURL)
	if norm == "" {
		return false
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	if q.seen[norm] {
		return false
	}

	now := time.Now().UTC()
	item := &QueueItem{
		URL:       norm,
		Depth:     depth,
		Status:    StatusPending,
		AddedAt:   now,
		UpdatedAt: now,
	}

	q.seen[norm] = true
	q.urlToIndex[norm] = len(q.items)
	q.items = append(q.items, item)
	return true
}

// EnqueueBatch adds multiple URLs at the specified depth. Returns number of newly enqueued URLs.
func (q *Queue) EnqueueBatch(urls []string, depth int) int {
	added := 0
	for _, u := range urls {
		if q.Enqueue(u, depth) {
			added++
		}
	}
	return added
}

// Pop retrieves the next pending QueueItem and marks it as processing. Returns nil if queue empty.
func (q *Queue) Pop() *QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, item := range q.items {
		if item.Status == StatusPending {
			item.Status = StatusProcessing
			item.UpdatedAt = time.Now().UTC()
			return item
		}
	}
	return nil
}

// MarkDone sets status of the given URL to done.
func (q *Queue) MarkDone(targetURL string) {
	norm := NormalizeURL(targetURL)
	q.mu.Lock()
	defer q.mu.Unlock()

	if idx, found := q.urlToIndex[norm]; found {
		q.items[idx].Status = StatusDone
		q.items[idx].UpdatedAt = time.Now().UTC()
	}
}

// MarkFailed sets status of the given URL to failed with error message.
func (q *Queue) MarkFailed(targetURL string, err error) {
	norm := NormalizeURL(targetURL)
	q.mu.Lock()
	defer q.mu.Unlock()

	if idx, found := q.urlToIndex[norm]; found {
		q.items[idx].Status = StatusFailed
		if err != nil {
			q.items[idx].Error = err.Error()
		} else {
			q.items[idx].Error = "unknown failure"
		}
		q.items[idx].UpdatedAt = time.Now().UTC()
	}
}

// HasPending returns true if there are pending items remaining in queue.
func (q *Queue) HasPending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, item := range q.items {
		if item.Status == StatusPending {
			return true
		}
	}
	return false
}

// Stats returns current status statistics for the queue.
func (q *Queue) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()

	stats := QueueStats{Total: len(q.items)}
	for _, item := range q.items {
		switch item.Status {
		case StatusPending:
			stats.Pending++
		case StatusProcessing:
			stats.Processing++
		case StatusDone:
			stats.Done++
		case StatusFailed:
			stats.Failed++
		}
	}
	return stats
}

// Items returns a copy of all current queue items.
func (q *Queue) Items() []QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()

	res := make([]QueueItem, len(q.items))
	for i, item := range q.items {
		res[i] = *item
	}
	return res
}

// String provides a human-readable summary of queue state.
func (q *Queue) String() string {
	s := q.Stats()
	return fmt.Sprintf("Queue[Total: %d, Pending: %d, Done: %d, Failed: %d]", s.Total, s.Pending, s.Done, s.Failed)
}
