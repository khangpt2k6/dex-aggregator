package rpc

import (
	"context"
	"sync"
	"testing"
	"time"
)

// fakeClock lets the limiter and breaker tests advance time without sleeping,
// so the suite stays fast and deterministic.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestLimiterAllowsBurstThenThrottles(t *testing.T) {
	clock := newFakeClock()
	l := newLimiterWithClock(10, 3, clock)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("burst call %d: %v", i, err)
		}
	}

	// The burst is spent, and no time has passed, so the next call must wait.
	if d, ok := l.reserve(); ok {
		t.Errorf("fourth call admitted immediately, want throttled (delay %v)", d)
	}
}

func TestLimiterRefillsOverTime(t *testing.T) {
	clock := newFakeClock()
	l := newLimiterWithClock(10, 2, clock) // 10 per second, so one per 100ms

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := l.Wait(ctx); err != nil {
			t.Fatalf("burst call %d: %v", i, err)
		}
	}
	if _, ok := l.reserve(); ok {
		t.Fatal("bucket not empty after burst")
	}

	clock.Advance(100 * time.Millisecond)
	if _, ok := l.reserve(); !ok {
		t.Error("no token available after 100ms at 10 per second")
	}

	clock.Advance(1 * time.Second)
	got := 0
	for i := 0; i < 10; i++ {
		if _, ok := l.reserve(); ok {
			got++
		}
	}
	// A full second refills the bucket, but never past its burst capacity.
	if got != 2 {
		t.Errorf("refilled %d tokens after 1s, want capped at burst of 2", got)
	}
}

func TestLimiterRespectsContextCancellation(t *testing.T) {
	clock := newFakeClock()
	l := newLimiterWithClock(1, 1, clock)

	ctx := context.Background()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	if err := l.Wait(cancelled); err == nil {
		t.Error("Wait with cancelled context returned nil, want context error")
	}
}

func TestLimiterDeadlineShorterThanWait(t *testing.T) {
	clock := newFakeClock()
	l := newLimiterWithClock(1, 1, clock) // one per second

	ctx := context.Background()
	if err := l.Wait(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// The next token is a second away, but the caller only has 10ms.
	short, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	if err := l.Wait(short); err == nil {
		t.Error("Wait returned nil when the deadline precedes the next token, want error")
	}
}

func TestLimiterIsConcurrencySafe(t *testing.T) {
	l := NewLimiter(100000, 1000)

	var wg sync.WaitGroup
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := l.Wait(ctx); err != nil {
					t.Errorf("Wait: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
