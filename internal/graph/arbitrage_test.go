package graph

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

// A consistently priced market has no closed loop that returns more than it
// consumed, especially once fees are charged.
func TestFindArbitrageFindsNoneInConsistentMarket(t *testing.T) {
	// WETH = 2000 DAI, WETH = 2000 USDC, DAI = 1 USDC. Internally consistent.
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
	}, time.Now())

	probe := map[string]*big.Int{tWETH.Address: whole(1, tWETH)}

	if got := FindArbitrage(s, probe, 3); len(got) != 0 {
		t.Errorf("found %d cycles in a consistent market, want 0: %+v", len(got), got)
	}
}

// Mispricing one leg badly enough to clear the fees opens a real loop.
func TestFindArbitrageFindsMispricedCycle(t *testing.T) {
	// WETH = 2000 DAI and WETH = 2000 USDC as before, but DAI trades at 1.15
	// USDC instead of 1.00. Going WETH -> DAI -> USDC -> WETH now gains.
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_750_000),
	}, time.Now())

	probe := map[string]*big.Int{tWETH.Address: whole(1, tWETH)}

	cycles := FindArbitrage(s, probe, 3)
	if len(cycles) == 0 {
		t.Fatal("found no cycles in a mispriced market, want at least 1")
	}

	c := cycles[0]
	if c.ProfitBps <= 0 {
		t.Errorf("ProfitBps = %d, want positive", c.ProfitBps)
	}
	if len(c.Tokens) < 3 {
		t.Errorf("cycle tokens = %v, want at least 3", c.Tokens)
	}
	// A cycle must return to where it started.
	if !strings.EqualFold(c.Tokens[0], c.Tokens[len(c.Tokens)-1]) {
		t.Errorf("cycle %v does not close", c.Tokens)
	}
	if c.AmountOut.Cmp(c.AmountIn) <= 0 {
		t.Errorf("AmountOut %s, want greater than AmountIn %s", c.AmountOut, c.AmountIn)
	}
}

func TestFindArbitrageSortsByProfit(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_750_000),
		v2("wethwbtc", tWETH, tWBTC, 1000, 40),
		v2("wbtcusdc", tWBTC, tUSDC, 100, 6_000_000),
	}, time.Now())

	probe := map[string]*big.Int{
		tWETH.Address: whole(1, tWETH),
		tUSDC.Address: whole(2000, tUSDC),
	}

	cycles := FindArbitrage(s, probe, 3)
	for i := 1; i < len(cycles); i++ {
		if cycles[i-1].ProfitBps < cycles[i].ProfitBps {
			t.Errorf("cycle %d profit %d bps precedes cycle %d profit %d bps, want descending",
				i-1, cycles[i-1].ProfitBps, i, cycles[i].ProfitBps)
		}
	}
}

func TestFindArbitrageHandlesEmptyInput(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	if got := FindArbitrage(nil, map[string]*big.Int{tWETH.Address: whole(1, tWETH)}, 3); got != nil {
		t.Errorf("FindArbitrage(nil snapshot) = %v, want nil", got)
	}
	if got := FindArbitrage(s, nil, 3); len(got) != 0 {
		t.Errorf("FindArbitrage(nil probe) = %v, want empty", got)
	}
	if got := FindArbitrage(s, map[string]*big.Int{"0xnothing": whole(1, tWETH)}, 3); len(got) != 0 {
		t.Errorf("FindArbitrage(unknown probe token) = %v, want empty", got)
	}
	if got := FindArbitrage(s, map[string]*big.Int{tWETH.Address: whole(1, tWETH)}, 1); len(got) != 0 {
		t.Errorf("maxHops=1 cannot close a cycle, got %v, want empty", got)
	}
}

// The same loop discovered from a different starting token is the same
// opportunity, and reporting it twice would overstate what is available.
func TestFindArbitrageDeduplicatesRotations(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_750_000),
	}, time.Now())

	probe := map[string]*big.Int{
		tWETH.Address: whole(1, tWETH),
		tDAI.Address:  whole(2000, tDAI),
		tUSDC.Address: whole(2000, tUSDC),
	}

	cycles := FindArbitrage(s, probe, 3)

	seen := map[string]bool{}
	for _, c := range cycles {
		key := strings.Join(sortedLower(c.Pools), ",")
		if seen[key] {
			t.Errorf("duplicate cycle over pool set %s", key)
		}
		seen[key] = true
	}
}

func sortedLower(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
