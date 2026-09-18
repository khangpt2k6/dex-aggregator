// Package rpc talks to an Ethereum node without getting the service banned.
//
// Public JSON-RPC providers rate-limit aggressively, and a refresh that issued
// one call per pool would be throttled within seconds. Four things prevent
// that: a bounded worker pool so concurrency never exceeds what the plan
// allows, Multicall3 batching so many contract reads become one request, a
// token bucket so the sustained rate stays under the quota, and a circuit
// breaker so a provider that is already failing is not hammered further.
package rpc

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// clock is the time source, injected so tests do not sleep.
type clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Limiter is a token bucket.
//
// Tokens accrue at a fixed rate up to a burst capacity. A burst lets a refresh
// cycle issue its batch immediately, while the sustained rate is what the
// provider actually sees over time.
type Limiter struct {
	mu sync.Mutex

	ratePerSec float64
	burst      float64
	tokens     float64
	last       time.Time
	clock      clock
}

// NewLimiter returns a limiter admitting ratePerSec calls per second, allowing
// bursts of up to burst calls.
func NewLimiter(ratePerSec float64, burst int) *Limiter {
	return newLimiterWithClock(ratePerSec, burst, realClock{})
}

func newLimiterWithClock(ratePerSec float64, burst int, c clock) *Limiter {
	if ratePerSec <= 0 {
		ratePerSec = 1
	}
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		ratePerSec: ratePerSec,
		burst:      float64(burst),
		tokens:     float64(burst),
		last:       c.Now(),
		clock:      c,
	}
}

// Wait blocks until a token is available or ctx is done.
//
// It returns the context error rather than proceeding when a caller has given
// up, so that a cancelled request stops consuming provider quota.
func (l *Limiter) Wait(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		delay, ok := l.reserve()
		if ok {
			return nil
		}

		// If the caller's deadline lands before the next token, waiting is
		// pointless. Failing now frees the goroutine and reports the real
		// reason.
		if deadline, has := ctx.Deadline(); has {
			if l.clock.Now().Add(delay).After(deadline) {
				return fmt.Errorf("rpc: rate limit wait of %v exceeds context deadline: %w", delay, context.DeadlineExceeded)
			}
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// reserve takes a token if one is available. When none is, it reports how long
// until the next one accrues.
func (l *Limiter) reserve() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	elapsed := now.Sub(l.last)
	if elapsed > 0 {
		l.tokens += elapsed.Seconds() * l.ratePerSec
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		l.last = now
	}

	if l.tokens >= 1 {
		l.tokens--
		return 0, true
	}

	needed := 1 - l.tokens
	wait := time.Duration(needed / l.ratePerSec * float64(time.Second))
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return wait, false
}
