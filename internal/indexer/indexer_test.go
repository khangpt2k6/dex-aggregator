package indexer

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/cache"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

var (
	weth = dex.Token{Address: "0xweth", Symbol: "WETH", Decimals: 18}
	usdc = dex.Token{Address: "0xusdc", Symbol: "USDC", Decimals: 6}
	dai  = dex.Token{Address: "0xdai", Symbol: "DAI", Decimals: 18}
)

func pool(addr string, a, b dex.Token) dex.Pool {
	return dex.Pool{
		Address:  addr,
		Protocol: dex.SushiswapV2,
		Token0:   a,
		Token1:   b,
		FeeBps:   30,
		Reserve0: new(big.Int).Mul(big.NewInt(1000), pow10(int(a.Decimals))),
		Reserve1: new(big.Int).Mul(big.NewInt(2_000_000), pow10(int(b.Decimals))),
	}
}

// fakeSource is a pool source under test control.
type fakeSource struct {
	name  string
	pools []dex.Pool
	err   error
	delay time.Duration
	calls atomic.Int64

	mu sync.Mutex
}

func (f *fakeSource) Name() string { return f.name }

func (f *fakeSource) Pools(ctx context.Context) ([]dex.Pool, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return f.pools, nil
}

func (f *fakeSource) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func TestRefreshOncePublishesFromAllSources(t *testing.T) {
	a := &fakeSource{name: "a", pools: []dex.Pool{pool("p1", weth, usdc)}}
	b := &fakeSource{name: "b", pools: []dex.Pool{pool("p2", usdc, dai)}}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{a, b}, h, cache.NewNoopStore(), time.Minute)

	if err := idx.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}

	s := h.Get()
	if s == nil {
		t.Fatal("no snapshot published")
	}
	if got := len(s.Pools()); got != 2 {
		t.Errorf("snapshot has %d pools, want 2", got)
	}
	if got := s.TokenCount(); got != 3 {
		t.Errorf("snapshot has %d tokens, want 3", got)
	}
	if idx.LastError() != nil {
		t.Errorf("LastError() = %v, want nil", idx.LastError())
	}
}

// One failing venue must not blind the router to every other venue. This is
// the difference between a degraded aggregator and a down one.
func TestRefreshOnceToleratesOneFailingSource(t *testing.T) {
	good := &fakeSource{name: "good", pools: []dex.Pool{pool("p1", weth, usdc)}}
	bad := &fakeSource{name: "bad", err: errors.New("provider timeout")}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{good, bad}, h, cache.NewNoopStore(), time.Minute)

	if err := idx.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce returned %v, want success from the healthy source", err)
	}

	s := h.Get()
	if s == nil {
		t.Fatal("no snapshot published")
	}
	if got := len(s.Pools()); got != 1 {
		t.Errorf("snapshot has %d pools, want 1 from the healthy source", got)
	}

	if idx.LastError() == nil {
		t.Error("LastError() = nil, want the failure to be recorded")
	}
	if st := idx.Status(); st.HealthySources != 1 || st.TotalSources != 2 {
		t.Errorf("Status() = %d of %d healthy, want 1 of 2", st.HealthySources, st.TotalSources)
	}
}

// If every source fails, the previous graph keeps serving. Replacing it with
// an empty one would turn a data problem into an outage.
func TestRefreshOnceKeepsPreviousSnapshotWhenEverythingFails(t *testing.T) {
	src := &fakeSource{name: "flaky", pools: []dex.Pool{pool("p1", weth, usdc)}}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{src}, h, cache.NewNoopStore(), time.Minute)

	if err := idx.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	first := h.Get()
	if first == nil {
		t.Fatal("no snapshot after the first refresh")
	}

	src.setErr(errors.New("everything is on fire"))

	if err := idx.RefreshOnce(context.Background()); err == nil {
		t.Error("RefreshOnce returned nil when every source failed, want an error")
	}

	if got := h.Get(); got == nil {
		t.Fatal("snapshot was cleared when all sources failed")
	} else if got != first {
		t.Error("snapshot was replaced when all sources failed, want the previous one kept")
	}
}

func TestRefreshOnceWithNoSources(t *testing.T) {
	h := cache.NewHolder()
	idx := New(nil, h, cache.NewNoopStore(), time.Minute)

	if err := idx.RefreshOnce(context.Background()); err == nil {
		t.Error("RefreshOnce with no sources returned nil, want an error")
	}
	if !h.IsEmpty() {
		t.Error("a snapshot was published with no sources configured")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	src := &fakeSource{name: "a", pools: []dex.Pool{pool("p1", weth, usdc)}}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{src}, h, cache.NewNoopStore(), 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- idx.Run(ctx) }()

	// Let it tick a few times.
	time.Sleep(60 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run() = %v, want nil or context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return within a second of cancellation")
	}

	if n := src.calls.Load(); n < 2 {
		t.Errorf("source was polled %d times, want it to have ticked repeatedly", n)
	}
}

func TestRunRefreshesImmediatelyNotAfterFirstTick(t *testing.T) {
	src := &fakeSource{name: "a", pools: []dex.Pool{pool("p1", weth, usdc)}}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{src}, h, cache.NewNoopStore(), time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = idx.Run(ctx) }()

	deadline := time.After(time.Second)
	for h.IsEmpty() {
		select {
		case <-deadline:
			t.Fatal("Run did not publish a snapshot before the first tick elapsed")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

// storeSpy records what the indexer persists.
type storeSpy struct {
	cache.NoopStore
	mu     sync.Mutex
	saved  [][]dex.Pool
	loaded []dex.Pool
}

func (s *storeSpy) SavePools(ctx context.Context, pools []dex.Pool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saved = append(s.saved, pools)
	return nil
}

func (s *storeSpy) LoadPools(ctx context.Context) ([]dex.Pool, error) {
	return s.loaded, nil
}

func (s *storeSpy) savedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.saved)
}

func TestRefreshOncePersistsToStore(t *testing.T) {
	src := &fakeSource{name: "a", pools: []dex.Pool{pool("p1", weth, usdc)}}
	spy := &storeSpy{}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{src}, h, spy, time.Minute)

	if err := idx.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}

	if got := spy.savedCount(); got != 1 {
		t.Errorf("store received %d saves, want 1", got)
	}
}

// A cold instance should serve stored state rather than nothing while its
// first real refresh runs.
func TestWarmStartLoadsFromStore(t *testing.T) {
	spy := &storeSpy{loaded: []dex.Pool{pool("cached", weth, usdc)}}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{&fakeSource{name: "a"}}, h, spy, time.Minute)

	if err := idx.WarmStart(context.Background()); err != nil {
		t.Fatalf("WarmStart: %v", err)
	}

	s := h.Get()
	if s == nil {
		t.Fatal("WarmStart published no snapshot")
	}
	if got := len(s.Pools()); got != 1 {
		t.Errorf("warm snapshot has %d pools, want 1", got)
	}
}

func TestWarmStartWithEmptyStoreIsNotAnError(t *testing.T) {
	h := cache.NewHolder()
	idx := New([]dex.PoolSource{&fakeSource{name: "a"}}, h, cache.NewNoopStore(), time.Minute)

	if err := idx.WarmStart(context.Background()); err != nil {
		t.Errorf("WarmStart with an empty store = %v, want nil", err)
	}
	if !h.IsEmpty() {
		t.Error("WarmStart published a snapshot from an empty store")
	}
}

func TestStatusReportsRefreshMetadata(t *testing.T) {
	// A measurable delay, because Windows timer granularity reports a
	// sub-microsecond refresh as exactly zero and the duration assertion below
	// would then pass whether or not the field was ever set.
	src := &fakeSource{name: "a", pools: []dex.Pool{pool("p1", weth, usdc)}, delay: 5 * time.Millisecond}

	h := cache.NewHolder()
	idx := New([]dex.PoolSource{src}, h, cache.NewNoopStore(), time.Minute)

	if st := idx.Status(); st.Refreshes != 0 {
		t.Errorf("Refreshes = %d before any refresh, want 0", st.Refreshes)
	}

	if err := idx.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}

	st := idx.Status()
	if st.Refreshes != 1 {
		t.Errorf("Refreshes = %d, want 1", st.Refreshes)
	}
	if st.Pools != 1 {
		t.Errorf("Pools = %d, want 1", st.Pools)
	}
	if st.LastDuration <= 0 {
		t.Errorf("LastDuration = %v, want positive", st.LastDuration)
	}
	if st.SourceNames[0] != "a" {
		t.Errorf("SourceNames = %v, want [a]", st.SourceNames)
	}
}
