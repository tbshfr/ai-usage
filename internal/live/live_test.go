package live

import (
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
