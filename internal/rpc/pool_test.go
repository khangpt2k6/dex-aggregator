package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeTransport records how many calls were in flight at once, so the test can
// assert the pool really does bound concurrency rather than merely claiming to.
type fakeTransport struct {
	mu       sync.Mutex
	inFlight int
	peak     int

	calls atomic.Int64

	// respond is consulted per call. Returning an error exercises retry.
	respond func(n int64) (json.RawMessage, error)

	// hold, when set, keeps each call inside the transport until released,
	// which is what makes the peak-concurrency measurement meaningful.
	hold chan struct{}
}

func (f *fakeTransport) Call(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	f.mu.Lock()
	f.inFlight++
	if f.inFlight > f.peak {
		f.peak = f.inFlight
	}
	f.mu.Unlock()

	defer func() {
		f.mu.Lock()
		f.inFlight--
		f.mu.Unlock()
	}()

	n := f.calls.Add(1)

	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if f.respond != nil {
		return f.respond(n)
	}
	return json.RawMessage(`"ok"`), nil
}

func (f *fakeTransport) peakConcurrency() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

func fastConfig(workers int) PoolConfig {
	return PoolConfig{
		Workers:          workers,
		RatePerSec:       1_000_000, // effectively unlimited for these tests
		Burst:            1_000_000,
		MaxRetries:       2,
		BaseBackoff:      time.Millisecond,
		BreakerThreshold: 5,
		BreakerCooldown:  time.Minute,
	}
}

func TestPoolBoundsConcurrency(t *testing.T) {
	const workers = 4

	ft := &fakeTransport{hold: make(chan struct{})}
	p := NewPool(ft, fastConfig(workers))

	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = p.Do(context.Background(), "eth_call")
		}()
	}

	// Give the goroutines time to pile up against the worker limit, then let
	// them all through.
	time.Sleep(50 * time.Millisecond)
	peak := ft.peakConcurrency()
	close(ft.hold)
	wg.Wait()

	if peak > workers {
		t.Errorf("peak concurrency = %d, want at most %d", peak, workers)
	}
	if peak == 0 {
		t.Error("peak concurrency = 0, the transport was never called")
	}
}

func TestPoolRetriesTransientFailure(t *testing.T) {
	var attempts atomic.Int64

	ft := &fakeTransport{
		respond: func(n int64) (json.RawMessage, error) {
			if attempts.Add(1) < 3 {
				return nil, errors.New("429 too many requests")
			}
			return json.RawMessage(`"recovered"`), nil
		},
	}
	p := NewPool(ft, fastConfig(2))

	got, err := p.Do(context.Background(), "eth_call")
	if err != nil {
		t.Fatalf("Do() = %v, want success after retries", err)
	}
	if string(got) != `"recovered"` {
		t.Errorf("Do() = %s, want \"recovered\"", got)
	}
	if n := attempts.Load(); n != 3 {
		t.Errorf("attempts = %d, want 3 (initial plus 2 retries)", n)
	}
	if s := p.Stats(); s.Retried == 0 {
		t.Error("Stats().Retried = 0, want positive")
	}
}

func TestPoolGivesUpAfterMaxRetries(t *testing.T) {
	want := errors.New("provider exploded")

	ft := &fakeTransport{
		respond: func(n int64) (json.RawMessage, error) { return nil, want },
	}
	p := NewPool(ft, fastConfig(2))

	_, err := p.Do(context.Background(), "eth_call")
	if err == nil {
		t.Fatal("Do() = nil error, want failure")
	}
	if !errors.Is(err, want) {
		t.Errorf("Do() error = %v, want it to wrap %v", err, want)
	}
	// MaxRetries of 2 means three attempts in total.
	if n := ft.calls.Load(); n != 3 {
		t.Errorf("transport called %d times, want 3", n)
	}
}

func TestPoolBreakerRejectsWithoutCallingTransport(t *testing.T) {
	ft := &fakeTransport{
		respond: func(n int64) (json.RawMessage, error) { return nil, errors.New("down") },
	}

	cfg := fastConfig(2)
	cfg.MaxRetries = 0
	cfg.BreakerThreshold = 2
	p := NewPool(ft, cfg)

	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := p.Do(ctx, "eth_call"); err == nil {
			t.Fatalf("call %d succeeded, want failure", i)
		}
	}

	before := ft.calls.Load()

	_, err := p.Do(ctx, "eth_call")
	if !errors.Is(err, ErrCircuitOpen) {
		t.Errorf("Do() error = %v, want ErrCircuitOpen", err)
	}
	if after := ft.calls.Load(); after != before {
		t.Errorf("transport called %d more times while open, want 0", after-before)
	}
	if s := p.Stats(); s.Rejected == 0 {
		t.Error("Stats().Rejected = 0, want positive")
	}
	if got := p.BreakerState(); got != StateOpen {
		t.Errorf("BreakerState() = %s, want %s", got, StateOpen)
	}
}

func TestPoolRespectsContextCancellation(t *testing.T) {
	ft := &fakeTransport{hold: make(chan struct{})}
	defer close(ft.hold)

	p := NewPool(ft, fastConfig(1))

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := p.Do(ctx, "eth_call"); err == nil {
		t.Error("Do() = nil error with an expiring context, want error")
	}
}

func TestPoolStatsCountCompleted(t *testing.T) {
	ft := &fakeTransport{}
	p := NewPool(ft, fastConfig(4))

	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := p.Do(ctx, "eth_call"); err != nil {
			t.Fatalf("Do(): %v", err)
		}
	}

	s := p.Stats()
	if s.Completed != 10 {
		t.Errorf("Stats().Completed = %d, want 10", s.Completed)
	}
	if s.InFlight != 0 {
		t.Errorf("Stats().InFlight = %d after all calls returned, want 0", s.InFlight)
	}
}

func TestPoolIsSafeUnderConcurrentUse(t *testing.T) {
	ft := &fakeTransport{}
	p := NewPool(ft, fastConfig(8))

	var wg sync.WaitGroup
	ctx := context.Background()

	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				if _, err := p.Do(ctx, "eth_call"); err != nil {
					t.Errorf("Do(): %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	if s := p.Stats(); s.Completed != 320 {
		t.Errorf("Stats().Completed = %d, want 320", s.Completed)
	}
}
