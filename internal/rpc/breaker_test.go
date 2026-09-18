package rpc

import (
	"testing"
	"time"
)

func TestBreakerStartsClosed(t *testing.T) {
	b := newBreakerWithClock(3, time.Second, newFakeClock())

	if !b.Allow() {
		t.Error("Allow() = false on a fresh breaker, want true")
	}
	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %s, want %s", got, StateClosed)
	}
}

func TestBreakerOpensAfterThreshold(t *testing.T) {
	b := newBreakerWithClock(3, time.Second, newFakeClock())

	b.Failure()
	b.Failure()
	if got := b.State(); got != StateClosed {
		t.Errorf("State() after 2 of 3 failures = %s, want %s", got, StateClosed)
	}

	b.Failure()
	if got := b.State(); got != StateOpen {
		t.Errorf("State() after 3 failures = %s, want %s", got, StateOpen)
	}
	if b.Allow() {
		t.Error("Allow() = true while open, want false")
	}
}

// A success before the threshold means the provider is fine, so the count of
// consecutive failures resets. Otherwise a slow trickle of unrelated errors
// would eventually trip a healthy provider.
func TestBreakerSuccessResetsFailureCount(t *testing.T) {
	b := newBreakerWithClock(3, time.Second, newFakeClock())

	b.Failure()
	b.Failure()
	b.Success()
	b.Failure()
	b.Failure()

	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %s, want %s after a success broke the run", got, StateClosed)
	}
}

func TestBreakerHalfOpensAfterCooldown(t *testing.T) {
	clock := newFakeClock()
	b := newBreakerWithClock(2, time.Second, clock)

	b.Failure()
	b.Failure()
	if b.Allow() {
		t.Fatal("breaker admitted a call while open")
	}

	clock.Advance(999 * time.Millisecond)
	if b.Allow() {
		t.Error("breaker admitted a call before the cooldown elapsed")
	}

	clock.Advance(2 * time.Millisecond)
	if !b.Allow() {
		t.Fatal("breaker did not half-open after the cooldown")
	}
	if got := b.State(); got != StateHalfOpen {
		t.Errorf("State() = %s, want %s", got, StateHalfOpen)
	}

	// Half-open admits exactly one probe. A second caller must still wait,
	// otherwise the cooldown would release a thundering herd at a provider
	// that may still be down.
	if b.Allow() {
		t.Error("half-open breaker admitted a second probe, want only one")
	}
}

func TestBreakerProbeSuccessCloses(t *testing.T) {
	clock := newFakeClock()
	b := newBreakerWithClock(2, time.Second, clock)

	b.Failure()
	b.Failure()
	clock.Advance(2 * time.Second)

	if !b.Allow() {
		t.Fatal("breaker did not half-open")
	}
	b.Success()

	if got := b.State(); got != StateClosed {
		t.Errorf("State() = %s after a successful probe, want %s", got, StateClosed)
	}
	if !b.Allow() {
		t.Error("closed breaker refused a call")
	}
}

func TestBreakerProbeFailureReopens(t *testing.T) {
	clock := newFakeClock()
	b := newBreakerWithClock(2, time.Second, clock)

	b.Failure()
	b.Failure()
	clock.Advance(2 * time.Second)

	if !b.Allow() {
		t.Fatal("breaker did not half-open")
	}
	b.Failure()

	if got := b.State(); got != StateOpen {
		t.Errorf("State() = %s after a failed probe, want %s", got, StateOpen)
	}
	if b.Allow() {
		t.Error("breaker admitted a call right after a failed probe")
	}

	// The cooldown restarts from the failed probe, not from the original trip.
	clock.Advance(2 * time.Second)
	if !b.Allow() {
		t.Error("breaker did not half-open again after a second cooldown")
	}
}

func TestBreakerZeroThresholdNeverOpens(t *testing.T) {
	b := newBreakerWithClock(0, time.Second, newFakeClock())

	for i := 0; i < 100; i++ {
		b.Failure()
	}
	if !b.Allow() {
		t.Error("breaker with threshold 0 opened, want it disabled")
	}
}
