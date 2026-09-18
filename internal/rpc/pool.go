package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"
)

// ErrCircuitOpen is returned when the breaker rejected a call without
// attempting it.
var ErrCircuitOpen = errors.New("rpc: circuit breaker is open")

// Transport is the minimal JSON-RPC surface the pool needs.
//
// Keeping it this small means the tests drive a fake rather than a real node,
// and the pool's own logic is what gets tested rather than go-ethereum's.
type Transport interface {
	Call(ctx context.Context, method string, params ...any) (json.RawMessage, error)
}

// PoolConfig tunes the pool. The defaults in internal/config are sized for a
// free-tier provider.
type PoolConfig struct {
	// Workers bounds how many calls may be in flight at once.
	Workers int

	// RatePerSec and Burst configure the token bucket.
	RatePerSec float64
	Burst      int

	// MaxRetries is how many times to retry after the first attempt.
	MaxRetries int

	// BaseBackoff is the first retry delay; it doubles each attempt.
	BaseBackoff time.Duration

	// BreakerThreshold is consecutive failures before the breaker opens.
	BreakerThreshold int

	// BreakerCooldown is how long the breaker stays open before probing.
	BreakerCooldown time.Duration
}

func (c *PoolConfig) withDefaults() {
	if c.Workers < 1 {
		c.Workers = 8
	}
	if c.RatePerSec <= 0 {
		c.RatePerSec = 10
	}
	if c.Burst < 1 {
		c.Burst = c.Workers
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 100 * time.Millisecond
	}
	if c.BreakerCooldown <= 0 {
		c.BreakerCooldown = 10 * time.Second
	}
}

// PoolStats is a snapshot of pool activity, surfaced on the health endpoint.
type PoolStats struct {
	InFlight  int64 `json:"inFlight"`
	Completed int64 `json:"completed"`
	Failed    int64 `json:"failed"`
	Retried   int64 `json:"retried"`
	Rejected  int64 `json:"rejected"`
}

// Pool issues JSON-RPC calls under a concurrency bound, a rate limit, and a
// circuit breaker.
//
// The concurrency bound is a buffered channel used as a semaphore rather than a
// fixed set of worker goroutines reading a queue. Both bound concurrency, but
// the semaphore lets each caller keep its own context and return its own error,
// which matters because a cancelled request should stop consuming quota
// immediately rather than waiting for a queued job to be picked up.
type Pool struct {
	transport Transport
	cfg       PoolConfig

	sem     chan struct{}
	limiter *Limiter
	breaker *Breaker

	inFlight  atomic.Int64
	completed atomic.Int64
	failed    atomic.Int64
	retried   atomic.Int64
	rejected  atomic.Int64

	// rngSeed drives retry jitter. Jitter matters because without it, a batch
	// of calls that fail together retry together, reproducing the same burst
	// that caused the failure.
	rngSeed atomic.Int64
}

// NewPool returns a pool over the given transport.
func NewPool(t Transport, cfg PoolConfig) *Pool {
	cfg.withDefaults()

	p := &Pool{
		transport: t,
		cfg:       cfg,
		sem:       make(chan struct{}, cfg.Workers),
		limiter:   NewLimiter(cfg.RatePerSec, cfg.Burst),
		breaker:   NewBreaker(cfg.BreakerThreshold, cfg.BreakerCooldown),
	}
	p.rngSeed.Store(time.Now().UnixNano())
	return p
}

// Do issues one RPC call, retrying transient failures.
func (p *Pool) Do(ctx context.Context, method string, params ...any) (json.RawMessage, error) {
	// Take a worker slot first. Waiting here rather than inside the retry loop
	// means a retrying call does not hold a slot while it sleeps.
	select {
	case p.sem <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-p.sem }()

	p.inFlight.Add(1)
	defer p.inFlight.Add(-1)

	var lastErr error

	for attempt := 0; attempt <= p.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			p.retried.Add(1)
			if err := p.sleepBackoff(ctx, attempt); err != nil {
				return nil, err
			}
		}

		if !p.breaker.Allow() {
			p.rejected.Add(1)
			return nil, fmt.Errorf("%w (provider failing, not attempting %s)", ErrCircuitOpen, method)
		}

		if err := p.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rpc: rate limiter: %w", err)
		}

		out, err := p.transport.Call(ctx, method, params...)
		if err == nil {
			p.breaker.Success()
			p.completed.Add(1)
			return out, nil
		}

		p.breaker.Failure()
		lastErr = err

		// A cancelled or expired context is the caller giving up, not the
		// provider failing. Retrying would waste quota on a dead request.
		if ctx.Err() != nil {
			break
		}
	}

	p.failed.Add(1)
	return nil, fmt.Errorf("rpc: %s failed after %d attempts: %w", method, p.cfg.MaxRetries+1, lastErr)
}

// sleepBackoff waits out an exponential backoff with jitter.
func (p *Pool) sleepBackoff(ctx context.Context, attempt int) error {
	backoff := p.cfg.BaseBackoff << (attempt - 1)

	// Full jitter. Without it a batch that failed together retries together,
	// which recreates the burst that caused the failure in the first place.
	seed := p.rngSeed.Add(1)
	jittered := time.Duration(rand.New(rand.NewSource(seed)).Int63n(int64(backoff) + 1))

	timer := time.NewTimer(jittered)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Stats returns current counters.
func (p *Pool) Stats() PoolStats {
	return PoolStats{
		InFlight:  p.inFlight.Load(),
		Completed: p.completed.Load(),
		Failed:    p.failed.Load(),
		Retried:   p.retried.Load(),
		Rejected:  p.rejected.Load(),
	}
}

// BreakerState reports how the pool currently regards the provider.
func (p *Pool) BreakerState() State { return p.breaker.State() }
