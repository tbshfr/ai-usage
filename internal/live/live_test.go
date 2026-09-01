package live

import (
	"context"
	"testing"
	"time"
)

const testTimeout = 2 * time.Second

func expectSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(testTimeout):
		t.Fatal("timed out waiting for signal")
	}
}

func expectNoSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("unexpected signal")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestNotifyReachesAllSubscribers(t *testing.T) {
	h := New()
	ch1, cancel1 := h.Subscribe()
	defer cancel1()
	ch2, cancel2 := h.Subscribe()
	defer cancel2()

	h.Notify()
	expectSignal(t, ch1)
	expectSignal(t, ch2)
}

func TestNotifyCoalescesPendingSignals(t *testing.T) {
	h := New()
	ch, cancel := h.Subscribe()
	defer cancel()

	h.Notify()
	h.Notify()
	h.Notify()
	expectSignal(t, ch)
	// The channel buffers exactly one: the burst collapses, the drained
	// subscriber stays current and never accumulates backlog.
	expectNoSignal(t, ch)
}

func TestCancelStopsDelivery(t *testing.T) {
	h := New()
	ch, cancel := h.Subscribe()
	cancel()
	h.Notify()
	expectNoSignal(t, ch)
}

func TestZeroHubIsUsable(t *testing.T) {
	var h Hub
	ch, cancel := h.Subscribe()
	defer cancel()
	h.Notify()
	expectSignal(t, ch)
}

func TestInterruptStreamsCancelsTrackedStreams(t *testing.T) {
	h := New()
	ctx, cancel := context.WithCancel(context.Background())
	untrack := h.TrackStream(cancel)
	defer untrack()

	h.InterruptStreams()
	select {
	case <-ctx.Done():
	case <-time.After(testTimeout):
		t.Fatal("tracked stream was not canceled by InterruptStreams")
	}

	// The registry is cleared, so untrack after the interrupt is a no-op
	// and a second interrupt is safe.
	untrack()
	h.InterruptStreams()
}

func TestUntrackRemovesStream(t *testing.T) {
	h := New()
	ctx, cancel := context.WithCancel(context.Background())
	untrack := h.TrackStream(cancel)
	untrack()

	h.InterruptStreams()
	expectNoCancel(t, ctx.Done())
}

// expectNoCancel asserts the context stays live; InterruptStreams must
// only affect still-tracked streams.
func expectNoCancel(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("untracked stream was canceled")
	case <-time.After(100 * time.Millisecond):
	}
}
