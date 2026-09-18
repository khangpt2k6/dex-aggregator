package graph

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

func whole(n int64, t dex.Token) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), pow10(int(t.Decimals)))
}

func routeSymbols(r *Route) []string {
	out := []string{r.Hops[0].TokenInSymbol}
	for _, h := range r.Hops {
		out = append(out, h.TokenOutSymbol)
	}
	return out
}

func mustRoute(t *testing.T, s *Snapshot, from, to dex.Token, amount *big.Int, maxHops int) *Route {
	t.Helper()
	r, err := FindBestRoute(s, from.Address, to.Address, amount, maxHops)
	if err != nil {
		t.Fatalf("FindBestRoute(%s->%s, %s): %v", from.Symbol, to.Symbol, amount, err)
	}
	return r
}

func TestFindBestRouteDirectSingleHop(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	r := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 3)

	if len(r.Hops) != 1 {
		t.Fatalf("got %d hops, want 1", len(r.Hops))
	}
	if r.Hops[0].PoolAddress != "p1" {
		t.Errorf("pool = %s, want p1", r.Hops[0].PoolAddress)
	}
	if r.AmountOut.Sign() <= 0 {
		t.Errorf("AmountOut = %s, want positive", r.AmountOut)
	}
	if r.AmountIn.Cmp(whole(1, tWETH)) != 0 {
		t.Errorf("AmountIn = %s, want %s", r.AmountIn, whole(1, tWETH))
	}
}

// A two-hop path through DAI beats the direct pool. A router that stopped at
// direct pools, or that capped itself at one hop, would leave value on the
// table.
func TestFindBestRoutePrefersTwoHopsWhenBetter(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("direct", tWETH, tUSDC, 1000, 1_800_000),
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
	}, time.Now())

	r := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 3)

	if len(r.Hops) != 2 {
		t.Fatalf("got %d hops via %v, want 2", len(r.Hops), routeSymbols(r))
	}
	if got := routeSymbols(r); got[1] != "DAI" {
		t.Errorf("route = %v, want to pass through DAI", got)
	}

	direct := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 1)
	if r.AmountOut.Cmp(direct.AmountOut) <= 0 {
		t.Errorf("two-hop out %s, want more than direct out %s", r.AmountOut, direct.AmountOut)
	}
}

// This is the test the whole design exists for.
//
// Pool "shallow" quotes a better price at zero size but holds little
// liquidity. Pool "deep" quotes worse at zero size but absorbs size without
// moving. The correct router picks shallow for a small trade and deep for a
// large one. A router using static spot-price edge weights picks shallow both
// times, and is badly wrong on the large trade.
func TestFindBestRouteDependsOnTradeSize(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("shallow", tWETH, tUSDC, 10, 25_000),
		v2("deep", tWETH, tUSDC, 10_000, 20_000_000),
	}, time.Now())

	small := mustRoute(t, s, tWETH, tUSDC, new(big.Int).Div(whole(1, tWETH), big.NewInt(100)), 1)
	if small.Hops[0].PoolAddress != "shallow" {
		t.Errorf("small trade routed through %s, want shallow (better rate at small size)", small.Hops[0].PoolAddress)
	}

	large := mustRoute(t, s, tWETH, tUSDC, whole(100, tWETH), 1)
	if large.Hops[0].PoolAddress != "deep" {
		t.Errorf("large trade routed through %s, want deep (shallow pool cannot absorb 100 WETH)", large.Hops[0].PoolAddress)
	}

	// Confirm the premise: the shallow pool really is the better spot price,
	// so this test would fail against a spot-price router rather than passing
	// by accident.
	shallowSmall := small.AmountOut
	deepSmall, err := FindBestRoute(
		BuildSnapshot([]dex.Pool{v2("deep", tWETH, tUSDC, 10_000, 20_000_000)}, time.Now()),
		tWETH.Address, tUSDC.Address,
		new(big.Int).Div(whole(1, tWETH), big.NewInt(100)), 1,
	)
	if err != nil {
		t.Fatalf("deep-only route: %v", err)
	}
	if shallowSmall.Cmp(deepSmall.AmountOut) <= 0 {
		t.Fatal("premise broken: shallow pool is not the better price at small size")
	}
}

func TestFindBestRouteRespectsMaxHops(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("direct", tWETH, tUSDC, 1000, 1_800_000),
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
	}, time.Now())

	r := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 1)
	if len(r.Hops) != 1 {
		t.Errorf("maxHops=1 returned %d hops, want 1", len(r.Hops))
	}
	if r.Hops[0].PoolAddress != "direct" {
		t.Errorf("maxHops=1 used pool %s, want direct", r.Hops[0].PoolAddress)
	}
}

func TestFindBestRouteThreeHops(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
		v2("usdcwbtc", tUSDC, tWBTC, 4_000_000, 100),
	}, time.Now())

	r := mustRoute(t, s, tWETH, tWBTC, whole(1, tWETH), 3)

	if len(r.Hops) != 3 {
		t.Fatalf("got %d hops via %v, want 3", len(r.Hops), routeSymbols(r))
	}
	if r.AmountOut.Sign() <= 0 {
		t.Errorf("AmountOut = %s, want positive", r.AmountOut)
	}
}

func TestFindBestRouteNoPath(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("daiwbtc", tDAI, tWBTC, 1_000_000, 25),
	}, time.Now())

	_, err := FindBestRoute(s, tWETH.Address, tWBTC.Address, whole(1, tWETH), 3)
	if !errors.Is(err, ErrNoRoute) {
		t.Errorf("error = %v, want ErrNoRoute", err)
	}
}

func TestFindBestRouteUnknownToken(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	if _, err := FindBestRoute(s, "0xnothing", tUSDC.Address, whole(1, tWETH), 3); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("unknown tokenIn error = %v, want ErrUnknownToken", err)
	}
	if _, err := FindBestRoute(s, tWETH.Address, "0xnothing", whole(1, tWETH), 3); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("unknown tokenOut error = %v, want ErrUnknownToken", err)
	}
}

func TestFindBestRouteRejectsBadInput(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	if _, err := FindBestRoute(s, tWETH.Address, tUSDC.Address, big.NewInt(0), 3); err == nil {
		t.Error("zero amount returned nil error, want error")
	}
	if _, err := FindBestRoute(s, tWETH.Address, tUSDC.Address, whole(1, tWETH), 0); err == nil {
		t.Error("maxHops=0 returned nil error, want error")
	}
	if _, err := FindBestRoute(nil, tWETH.Address, tUSDC.Address, whole(1, tWETH), 3); err == nil {
		t.Error("nil snapshot returned nil error, want error")
	}
	if _, err := FindBestRoute(s, tWETH.Address, tWETH.Address, whole(1, tWETH), 3); err == nil {
		t.Error("same token in and out returned nil error, want error")
	}
}

// Hop amounts must chain: what leaves one hop is what enters the next. If they
// do not, the reported route is not the route that was priced.
func TestRouteHopsChainCorrectly(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
	}, time.Now())

	in := whole(1, tWETH)
	r := mustRoute(t, s, tWETH, tUSDC, in, 3)

	if r.Hops[0].AmountIn.Cmp(in) != 0 {
		t.Errorf("first hop AmountIn = %s, want %s", r.Hops[0].AmountIn, in)
	}
	for i := 1; i < len(r.Hops); i++ {
		if r.Hops[i].AmountIn.Cmp(r.Hops[i-1].AmountOut) != 0 {
			t.Errorf("hop %d AmountIn %s != hop %d AmountOut %s", i, r.Hops[i].AmountIn, i-1, r.Hops[i-1].AmountOut)
		}
		if !strings.EqualFold(r.Hops[i].TokenIn, r.Hops[i-1].TokenOut) {
			t.Errorf("hop %d TokenIn %s != hop %d TokenOut %s", i, r.Hops[i].TokenIn, i-1, r.Hops[i-1].TokenOut)
		}
	}

	last := r.Hops[len(r.Hops)-1]
	if r.AmountOut.Cmp(last.AmountOut) != 0 {
		t.Errorf("Route.AmountOut = %s, want last hop AmountOut %s", r.AmountOut, last.AmountOut)
	}
	if !strings.EqualFold(last.TokenOut, tUSDC.Address) {
		t.Errorf("final TokenOut = %s, want USDC", last.TokenOut)
	}
}

// In a market that charges fees, revisiting a token always loses value, so the
// optimal route never contains one.
func TestRouteDoesNotRevisitTokens(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
		v2("usdcweth", tUSDC, tWETH, 2_000_000, 1000),
		v2("wbtcdai", tWBTC, tDAI, 50, 2_000_000),
	}, time.Now())

	r := mustRoute(t, s, tWETH, tWBTC, whole(1, tWETH), 4)

	seen := map[string]bool{}
	for _, sym := range routeSymbols(r) {
		if seen[sym] {
			t.Errorf("route %v revisits %s", routeSymbols(r), sym)
		}
		seen[sym] = true
	}
}

func TestPriceImpactGrowsWithSize(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	small := mustRoute(t, s, tWETH, tUSDC, new(big.Int).Div(whole(1, tWETH), big.NewInt(100)), 1)
	large := mustRoute(t, s, tWETH, tUSDC, whole(200, tWETH), 1)

	if small.PriceImpactBps < 0 {
		t.Errorf("small trade impact = %d bps, want non-negative", small.PriceImpactBps)
	}
	if small.PriceImpactBps > 50 {
		t.Errorf("0.01 WETH into a 1000 WETH pool = %d bps impact, want under 50", small.PriceImpactBps)
	}
	if large.PriceImpactBps <= small.PriceImpactBps {
		t.Errorf("large impact %d bps, want greater than small impact %d bps", large.PriceImpactBps, small.PriceImpactBps)
	}
	if large.PriceImpactBps < 500 {
		t.Errorf("200 WETH into a 1000 WETH pool = %d bps impact, want well above 500", large.PriceImpactBps)
	}
}

// A pool that errors for the requested size (a V3 pool whose tick window runs
// out, say) must be skipped, not fail the whole request, as long as some other
// route exists.
func TestFindBestRouteSkipsUnquotablePool(t *testing.T) {
	broken := dex.Pool{
		Address: "broken", Protocol: dex.UniswapV3,
		Token0: tWETH, Token1: tUSDC, FeeBps: 30,
		SqrtPriceX96: big.NewInt(1), Liquidity: big.NewInt(1),
		Tick: 0, TickSpacing: 60,
		// No ticks at all, so any swap runs off the end of the window.
	}

	s := BuildSnapshot([]dex.Pool{broken, v2("good", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	r := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 2)
	if r.Hops[0].PoolAddress != "good" {
		t.Errorf("routed through %s, want good", r.Hops[0].PoolAddress)
	}
}

func TestRouteCarriesProtocolAndSymbols(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	r := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 1)
	h := r.Hops[0]

	if h.Protocol != dex.SushiswapV2 {
		t.Errorf("Protocol = %s, want %s", h.Protocol, dex.SushiswapV2)
	}
	if h.TokenInSymbol != "WETH" || h.TokenOutSymbol != "USDC" {
		t.Errorf("symbols = %s->%s, want WETH->USDC", h.TokenInSymbol, h.TokenOutSymbol)
	}
	if h.FeeBps != 30 {
		t.Errorf("FeeBps = %d, want 30", h.FeeBps)
	}
}

// The snapshot is shared and immutable. Routing must not write to it.
func TestFindBestRouteDoesNotMutateSnapshot(t *testing.T) {
	pools := []dex.Pool{
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_000_000),
	}
	s := BuildSnapshot(pools, time.Now())

	before := s.Pools()
	beforeReserves := make([]string, len(before))
	for i, p := range before {
		beforeReserves[i] = p.Reserve0.String() + "/" + p.Reserve1.String()
	}

	for i := 0; i < 5; i++ {
		mustRoute(t, s, tWETH, tUSDC, whole(int64(i+1), tWETH), 3)
	}

	after := s.Pools()
	for i, p := range after {
		got := p.Reserve0.String() + "/" + p.Reserve1.String()
		if got != beforeReserves[i] {
			t.Errorf("pool %s reserves changed: %s -> %s", p.Address, beforeReserves[i], got)
		}
	}
}

// Regression: the simulator produced a graph with a profitable loop, and the
// router folded that loop into an ordinary swap quote, returning
// WETH -> WBTC -> WETH -> WBTC and using the same pool twice.
//
// That quote is not executable. Every hop is priced against one snapshot, so
// the second pass through a pool would actually meet the price the first pass
// just moved. A swap route has to be a simple path; profitable loops belong to
// FindArbitrage, which looks for them deliberately.
func TestRouteNeverReusesAPoolOrRevisitsAToken(t *testing.T) {
	// DAI is deliberately mispriced against USDC so a WETH/USDC/DAI loop pays
	// more than it costs, which is what tempted the router before.
	s := BuildSnapshot([]dex.Pool{
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_750_000),
		v2("usdcwbtc", tUSDC, tWBTC, 4_000_000, 100),
	}, time.Now())

	// Confirm the premise: a profitable loop really is present in this graph,
	// so the test would catch a regression rather than passing vacuously.
	probe := map[string]*big.Int{tWETH.Address: whole(1, tWETH)}
	if len(FindArbitrage(s, probe, 4)) == 0 {
		t.Fatal("premise broken: this graph has no arbitrage loop to tempt the router")
	}

	for _, maxHops := range []int{2, 3, 4, 5} {
		r, err := FindBestRoute(s, tWETH.Address, tWBTC.Address, whole(1, tWETH), maxHops)
		if err != nil {
			t.Fatalf("maxHops=%d: %v", maxHops, err)
		}

		seenPool := map[string]bool{}
		for _, h := range r.Hops {
			if seenPool[h.PoolAddress] {
				t.Errorf("maxHops=%d: route %v uses pool %s twice, which cannot be priced from one snapshot",
					maxHops, routeSymbols(r), h.PoolAddress)
			}
			seenPool[h.PoolAddress] = true
		}

		seenToken := map[string]bool{}
		for _, sym := range routeSymbols(r) {
			if seenToken[sym] {
				t.Errorf("maxHops=%d: route %v revisits %s", maxHops, routeSymbols(r), sym)
			}
			seenToken[sym] = true
		}

		if !strings.EqualFold(r.Hops[len(r.Hops)-1].TokenOut, tWBTC.Address) {
			t.Errorf("maxHops=%d: route %v does not end at WBTC", maxHops, routeSymbols(r))
		}
	}
}

// A route must never start by selling something back into the token it came
// from, and must never end where it started.
func TestRouteNeverReturnsToTheInputToken(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{
		v2("wethusdc", tWETH, tUSDC, 1000, 2_000_000),
		v2("wethdai", tWETH, tDAI, 1000, 2_000_000),
		v2("daiusdc", tDAI, tUSDC, 5_000_000, 5_750_000),
	}, time.Now())

	r := mustRoute(t, s, tWETH, tUSDC, whole(1, tWETH), 4)

	for i, h := range r.Hops {
		if i > 0 && strings.EqualFold(h.TokenOut, tWETH.Address) {
			t.Errorf("route %v returns to the input token at hop %d", routeSymbols(r), i)
		}
	}
}
