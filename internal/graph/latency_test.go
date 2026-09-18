package graph

import (
	"math/big"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// latencyBudget is the promise the service makes about routing time over a
// warm snapshot. It excludes RPC and network time, which is why the indexer
// keeps the snapshot warm in the first place.
const latencyBudget = 10 * time.Millisecond

// TestRouterLatencyBudget fails the build if routing p99 regresses past the
// budget.
//
// A number in a README rots the moment someone changes a loop. A number in a
// test that fails the build does not, which is the only reason to trust the
// figure at all.
func TestRouterLatencyBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement is not meaningful under -short")
	}

	const (
		tokenCount = 40
		poolCount  = 300
		samples    = 2000
	)

	s := generateSnapshot(tokenCount, poolCount, 20260918)
	tokens := s.Tokens()

	t.Logf("graph: %d tokens, %d pools, %d directed edges", s.TokenCount(), len(s.Pools()), s.EdgeCount())

	rng := rand.New(rand.NewSource(1))
	amounts := []*big.Int{
		pow10(16),
		pow10(18),
		new(big.Int).Mul(big.NewInt(50), pow10(18)),
	}

	// Warm up so the first sample does not pay for lazily grown heap.
	for i := 0; i < 100; i++ {
		a, b := rng.Intn(len(tokens)), rng.Intn(len(tokens))
		if a == b {
			continue
		}
		_, _ = FindBestRoute(s, tokens[a].Address, tokens[b].Address, amounts[i%len(amounts)], 3)
	}

	durations := make([]time.Duration, 0, samples)
	routed := 0

	for i := 0; i < samples; i++ {
		a, b := rng.Intn(len(tokens)), rng.Intn(len(tokens))
		if a == b {
			continue
		}
		amount := amounts[i%len(amounts)]

		start := time.Now()
		route, err := FindBestRoute(s, tokens[a].Address, tokens[b].Address, amount, 3)
		elapsed := time.Since(start)

		durations = append(durations, elapsed)
		if err == nil && route != nil {
			routed++
		}
	}

	if len(durations) == 0 {
		t.Fatal("no samples collected")
	}
	// A budget met by failing fast would be worthless.
	if routed*100/len(durations) < 80 {
		t.Fatalf("only %d of %d queries returned a route; the budget is not being met by real work", routed, len(durations))
	}

	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })

	p50 := percentile(durations, 50)
	p95 := percentile(durations, 95)
	p99 := percentile(durations, 99)
	worst := durations[len(durations)-1]

	t.Logf("router latency over %d queries (%d routed): p50=%v p95=%v p99=%v max=%v",
		len(durations), routed, p50, p95, p99, worst)

	if p99 > latencyBudget {
		t.Errorf("router p99 = %v, want at most %v (p50=%v p95=%v max=%v)", p99, latencyBudget, p50, p95, worst)
	}
}

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (len(sorted)*p + 99) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func BenchmarkFindBestRoute(b *testing.B) {
	s := generateSnapshot(40, 300, 20260918)
	tokens := s.Tokens()
	amount := pow10(18)

	for _, maxHops := range []int{1, 2, 3, 4} {
		b.Run(hopsLabel(maxHops), func(b *testing.B) {
			rng := rand.New(rand.NewSource(7))
			b.ReportAllocs()
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				a, c := rng.Intn(len(tokens)), rng.Intn(len(tokens))
				if a == c {
					continue
				}
				_, _ = FindBestRoute(s, tokens[a].Address, tokens[c].Address, amount, maxHops)
			}
		})
	}
}

func BenchmarkBuildSnapshot(b *testing.B) {
	s := generateSnapshot(40, 300, 20260918)
	pools := s.Pools()
	now := time.Now()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildSnapshot(pools, now)
	}
}

func hopsLabel(n int) string {
	return "maxHops=" + string(rune('0'+n))
}
