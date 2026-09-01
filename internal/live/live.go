// Package live broadcasts "data changed" signals from the ingest
// pipeline to dashboard SSE clients. The payload carries nothing: each
// notification only means "the database gained at least one generation,
// re-fetch what you render". Losing one is harmless because the next
// signal follows, so slow subscribers drop instead of backing up.
package live

import "sync"

// Hub fans notifications out to subscriber channels. The zero value is
// ready to use; New is preferred for clarity.
type Hub struct {
	mu   sync.Mutex
	subs map[chan struct{}]struct{}
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{subs: make(map[chan struct{}]struct{})}
}

// Subscribe registers a new subscriber and returns its event channel plus
// a cancel function that removes it. The channel buffers one signal, so a
// subscriber that has not drained the previous one yet is skipped rather
// than blocked.
func (h *Hub) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	h.mu.Lock()
	if h.subs == nil {
		h.subs = make(map[chan struct{}]struct{})
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// Notify wakes every current subscriber once.
func (h *Hub) Notify() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
