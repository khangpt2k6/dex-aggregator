package rpc

import (
	"sync"
	"time"
)

// State is the breaker's current disposition toward the provider.
type State string

const (
	// StateClosed passes calls through. The provider is behaving.
	StateClosed State = "closed"

	// StateOpen rejects calls without attempting them. The provider is failing
	// and further traffic would only make things worse for both sides.
	StateOpen State = "open"

	// StateHalfOpen admits a single probe to find out whether the provider has
	// recovered, without releasing the full load at it.
	StateHalfOpen State = "half-open"
)

// Breaker stops a failing provider from being hammered.
//
// Retrying into an outage is worse than useless: it burns the caller's own
// quota, adds load to something already struggling, and delays the moment the
// system notices it should degrade instead. The breaker converts a slow pile of
// timeouts into a fast, cheap rejection, which is what lets the indexer keep
// serving the previous snapshot instead of stalling.
type Breaker struct {
	mu sync.Mutex

	threshold int
	cooldown  time.Duration
	clock     clock

	state       State
	failures    int
	openedAt    time.Time
	probeInFlig bool
}

// NewBreaker returns a breaker that opens after threshold consecutive failures
// and half-opens once cooldown has elapsed. A threshold of zero disables it.
func NewBreaker(threshold int, cooldown time.Duration) *Breaker {
	return newBreakerWithClock(threshold, cooldown, realClock{})
}

func newBreakerWithClock(threshold int, cooldown time.Duration, c clock) *Breaker {
	if cooldown <= 0 {
		cooldown = 5 * time.Second
	}
	return &Breaker{
		threshold: threshold,
		cooldown:  cooldown,
		clock:     c,
		state:     StateClosed,
	}
}

// Allow reports whether a call may proceed, and moves the breaker to half-open
// when the cooldown has elapsed.
func (b *Breaker) Allow() bool {
	if b.threshold <= 0 {
		return true
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	switch b.state {
	case StateClosed:
		return true

	case StateOpen:
		if b.clock.Now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		// Cooldown elapsed. Let exactly one caller through to find out whether
		// the provider is back.
		b.state = StateHalfOpen
		b.probeInFlig = true
		return true

	case StateHalfOpen:
		// A probe is already out. Releasing everyone here would be a
		// thundering herd at a provider that may still be down.
		if b.probeInFlig {
			return false
		}
		b.probeInFlig = true
		return true

	default:
		return true
	}
}

// Success records a call that worked.
func (b *Breaker) Success() {
	if b.threshold <= 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.failures = 0
	b.state = StateClosed
	b.probeInFlig = false
}

// Failure records a call that did not work, opening the breaker once failures
// reach the threshold.
func (b *Breaker) Failure() {
	if b.threshold <= 0 {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.state == StateHalfOpen {
		// The probe failed, so the provider is still down. Restart the
		// cooldown from now rather than from the original trip.
		b.state = StateOpen
		b.openedAt = b.clock.Now()
		b.probeInFlig = false
		return
	}

	b.failures++
	if b.failures >= b.threshold {
		b.state = StateOpen
		b.openedAt = b.clock.Now()
		b.probeInFlig = false
	}
}

// State returns the current state, for health reporting.
func (b *Breaker) State() State {
	if b.threshold <= 0 {
		return StateClosed
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}
