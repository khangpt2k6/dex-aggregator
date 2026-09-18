// Package indexer keeps the routable graph current.
//
// It is the slow half of the system. It polls every pool source, builds a new
// graph snapshot, and publishes it for the request path to read. Nothing here
// runs while a quote is being served, which is the entire reason quotes can
// hold a single-digit millisecond budget while the data behind them comes from
// a network round trip to an Ethereum node.
package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/cache"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/graph"
)

// Status is a summary of indexing health, surfaced on the health endpoint.
type Status struct {
	Refreshes      int64         `json:"refreshes"`
	Pools          int           `json:"pools"`
	Tokens         int           `json:"tokens"`
	Edges          int           `json:"edges"`
	HealthySources int           `json:"healthySources"`
	TotalSources   int           `json:"totalSources"`
	SourceNames    []string      `json:"sourceNames"`
	LastDuration   time.Duration `json:"-"`
	LastDurationMs int64         `json:"lastDurationMs"`
	LastError      string        `json:"lastError,omitempty"`
}

// Indexer polls pool sources and publishes graph snapshots.
type Indexer struct {
	sources  []dex.PoolSource
	holder   *cache.Holder
	store    cache.Store
	interval time.Duration
	log      *slog.Logger

	mu      sync.RWMutex
	status  Status
	lastErr error
}

// New returns an indexer. It does not start polling; call Run for that.
func New(sources []dex.PoolSource, h *cache.Holder, store cache.Store, interval time.Duration) *Indexer {
	if interval <= 0 {
		interval = 6 * time.Second
	}
	if store == nil {
		store = cache.NewNoopStore()
	}

	names := make([]string, 0, len(sources))
	for _, s := range sources {
		names = append(names, s.Name())
	}

	return &Indexer{
		sources:  sources,
		holder:   h,
		store:    store,
		interval: interval,
		log:      slog.Default(),
		status: Status{
			TotalSources: len(sources),
			SourceNames:  names,
		},
	}
}

// WithLogger sets the logger used for refresh reporting.
func (ix *Indexer) WithLogger(l *slog.Logger) *Indexer {
	if l != nil {
		ix.log = l
	}
	return ix
}

// Run refreshes on a ticker until ctx is done.
//
// It refreshes once immediately rather than waiting out the first tick, so a
// freshly started process is useful in milliseconds instead of seconds.
func (ix *Indexer) Run(ctx context.Context) error {
	if err := ix.RefreshOnce(ctx); err != nil {
		// A failed first refresh is not fatal. The loop keeps trying, and the
		// health endpoint reports the service as not ready in the meantime.
		ix.log.Warn("initial pool refresh failed", "err", err)
	}

	ticker := time.NewTicker(ix.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := ix.RefreshOnce(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				ix.log.Warn("pool refresh failed", "err", err)
			}
		}
	}
}

// RefreshOnce polls every source once and publishes the result.
//
// Sources are polled concurrently, and one failing source does not discard the
// pools from the others. An aggregator that went dark because a single venue
// timed out would be worse than one serving slightly narrower liquidity.
func (ix *Indexer) RefreshOnce(ctx context.Context) error {
	if len(ix.sources) == 0 {
		return errors.New("indexer: no pool sources configured")
	}

	start := time.Now()

	type result struct {
		name  string
		pools []dex.Pool
		err   error
	}

	results := make([]result, len(ix.sources))
	var wg sync.WaitGroup

	for i, src := range ix.sources {
		wg.Add(1)
		go func(i int, src dex.PoolSource) {
			defer wg.Done()
			pools, err := src.Pools(ctx)
			results[i] = result{name: src.Name(), pools: pools, err: err}
		}(i, src)
	}
	wg.Wait()

	var (
		merged  []dex.Pool
		healthy int
		failed  []string
	)
	for _, r := range results {
		if r.err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", r.name, r.err))
			continue
		}
		healthy++
		merged = append(merged, r.pools...)
	}

	elapsed := time.Since(start)

	// Every source failed. Keep the previous snapshot serving rather than
	// publishing an empty graph, which would turn a data problem into an
	// outage for every caller.
	if healthy == 0 {
		err := fmt.Errorf("indexer: all %d sources failed: %s", len(ix.sources), strings.Join(failed, "; "))
		ix.recordFailure(err, healthy, elapsed)
		return err
	}

	snapshot := graph.BuildSnapshot(merged, time.Now())
	ix.holder.Set(snapshot)

	ix.recordSuccess(snapshot, healthy, failed, elapsed)

	// Persist for warm starts. A store failure is logged, never fatal: Redis
	// being down should not stop the service from routing.
	if err := ix.store.SavePools(ctx, merged); err != nil {
		ix.log.Warn("persisting pools failed", "store", ix.store.Name(), "err", err)
	}

	return nil
}

// WarmStart publishes a snapshot from the store so a cold instance can serve
// traffic before its first live refresh completes.
//
// An empty store is not an error; it just means nothing has indexed yet.
func (ix *Indexer) WarmStart(ctx context.Context) error {
	pools, err := ix.store.LoadPools(ctx)
	if err != nil {
		return fmt.Errorf("indexer: warm start: %w", err)
	}
	if len(pools) == 0 {
		return nil
	}

	ix.holder.Set(graph.BuildSnapshot(pools, time.Now()))
	ix.log.Info("warm start from store", "store", ix.store.Name(), "pools", len(pools))
	return nil
}

func (ix *Indexer) recordSuccess(s *graph.Snapshot, healthy int, failed []string, elapsed time.Duration) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.status.Refreshes++
	ix.status.Pools = len(s.Pools())
	ix.status.Tokens = s.TokenCount()
	ix.status.Edges = s.EdgeCount()
	ix.status.HealthySources = healthy
	ix.status.LastDuration = elapsed
	ix.status.LastDurationMs = elapsed.Milliseconds()

	if len(failed) == 0 {
		ix.lastErr = nil
		ix.status.LastError = ""
		return
	}

	// A partial failure still counts as a successful refresh, but the reason
	// some liquidity is missing stays visible.
	ix.lastErr = errors.New(strings.Join(failed, "; "))
	ix.status.LastError = ix.lastErr.Error()
}

func (ix *Indexer) recordFailure(err error, healthy int, elapsed time.Duration) {
	ix.mu.Lock()
	defer ix.mu.Unlock()

	ix.lastErr = err
	ix.status.LastError = err.Error()
	ix.status.HealthySources = healthy
	ix.status.LastDuration = elapsed
	ix.status.LastDurationMs = elapsed.Milliseconds()
}

// LastError returns the most recent refresh problem, or nil if the last
// refresh was completely clean.
func (ix *Indexer) LastError() error {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.lastErr
}

// Status returns a copy of the current indexing status.
func (ix *Indexer) Status() Status {
	ix.mu.RLock()
	defer ix.mu.RUnlock()

	out := ix.status
	out.SourceNames = append([]string(nil), ix.status.SourceNames...)
	return out
}

// Interval returns the configured refresh period, which health checks use to
// judge how stale a snapshot is allowed to be.
func (ix *Indexer) Interval() time.Duration { return ix.interval }
