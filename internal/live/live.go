// Package live broadcasts "data changed" signals from the ingest
// pipeline to dashboard SSE clients. The payload carries nothing: each
// notification only means "the database gained at least one generation,
// re-fetch what you render". Losing one is harmless because the next
// signal follows, so slow subscribers drop instead of backing up.
package live

import (
	"context"
	"sync"
)

// Hub fans notifications out to subscriber channels. The zero value is
// ready to use; New is preferred for clarity.
type Hub struct {
	mu      sync.Mutex
	subs    map[chan struct{}]struct{}
	streams map[*streamHandle]struct{}
}

// streamHandle anchors a tracked SSE stream in the map (cancel funcs are
// not valid map keys, so the registration hangs off a pointer).
type streamHandle struct {
	cancel context.CancelFunc
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

// TrackStream registers a live SSE handler's cancel function so a graceful
// server shutdown can end every open stream. The returned untrack function
// removes the registration; handlers defer it.
func (h *Hub) TrackStream(cancel context.CancelFunc) (untrack func()) {
	handle := &streamHandle{cancel: cancel}
	h.mu.Lock()
	if h.streams == nil {
		h.streams = make(map[*streamHandle]struct{})
	}
	h.streams[handle] = struct{}{}
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		delete(h.streams, handle)
		h.mu.Unlock()
	}
}

// InterruptStreams cancels every tracked stream and drops the
// registrations. http.Server.Shutdown waits for active connections and
// never cancels request contexts, so without this a shutdown would always
// burn its whole grace period while a dashboard tab holds an SSE stream.
func (h *Hub) InterruptStreams() {
	h.mu.Lock()
	handles := make([]*streamHandle, 0, len(h.streams))
	for handle := range h.streams {
		handles = append(handles, handle)
	}
	h.streams = nil
	h.mu.Unlock()
	for _, handle := range handles {
		handle.cancel()
	}
}
