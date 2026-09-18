package graph

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

func tok(symbol string, decimals uint8) dex.Token {
	return dex.Token{Address: "0x" + strings.ToLower(symbol), Symbol: symbol, Decimals: decimals}
}

var (
	tWETH = tok("WETH", 18)
	tUSDC = tok("USDC", 6)
	tDAI  = tok("DAI", 18)
	tWBTC = tok("WBTC", 8)
)

// v2 builds a Sushiswap-style pool with the given whole-unit reserves.
func v2(addr string, a, b dex.Token, ra, rb int64) dex.Pool {
	return dex.Pool{
		Address:  addr,
		Protocol: dex.SushiswapV2,
		Token0:   a,
		Token1:   b,
		FeeBps:   30,
		Reserve0: new(big.Int).Mul(big.NewInt(ra), pow10(int(a.Decimals))),
		Reserve1: new(big.Int).Mul(big.NewInt(rb), pow10(int(b.Decimals))),
	}
}

func TestBuildSnapshotCountsTokensAndEdges(t *testing.T) {
	pools := []dex.Pool{
		v2("p1", tWETH, tUSDC, 1000, 2_000_000),
		v2("p2", tUSDC, tDAI, 1_000_000, 1_000_000),
		v2("p3", tWETH, tWBTC, 1000, 40),
	}

	at := time.Now()
	s := BuildSnapshot(pools, at)

	if got := s.TokenCount(); got != 4 {
		t.Errorf("TokenCount() = %d, want 4", got)
	}
	// Every pool is usable in both directions.
	if got := s.EdgeCount(); got != 6 {
		t.Errorf("EdgeCount() = %d, want 6", got)
	}
	if !s.BuiltAt().Equal(at) {
		t.Errorf("BuiltAt() = %v, want %v", s.BuiltAt(), at)
	}
	if got := len(s.Pools()); got != 3 {
		t.Errorf("len(Pools()) = %d, want 3", got)
	}
}

func TestEdgesFromReturnsBothPools(t *testing.T) {
	pools := []dex.Pool{
		v2("p1", tWETH, tUSDC, 1000, 2_000_000),
		v2("p2", tWETH, tDAI, 1000, 2_000_000),
	}
	s := BuildSnapshot(pools, time.Now())

	edges := s.EdgesFrom(tWETH.Address)
	if len(edges) != 2 {
		t.Fatalf("EdgesFrom(WETH) returned %d edges, want 2", len(edges))
	}

	dests := map[string]bool{}
	for _, e := range edges {
		if !strings.EqualFold(e.From, tWETH.Address) {
			t.Errorf("edge From = %s, want WETH", e.From)
		}
		dests[strings.ToLower(e.To)] = true
	}
	if !dests[strings.ToLower(tUSDC.Address)] || !dests[strings.ToLower(tDAI.Address)] {
		t.Errorf("edges lead to %v, want USDC and DAI", dests)
	}
}

func TestEdgesFromIsCaseInsensitive(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	if got := len(s.EdgesFrom(strings.ToUpper(tWETH.Address))); got != 1 {
		t.Errorf("EdgesFrom(uppercase) = %d edges, want 1", got)
	}
}

func TestEdgesFromUnknownToken(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	if got := s.EdgesFrom("0xnothing"); len(got) != 0 {
		t.Errorf("EdgesFrom(unknown) = %d edges, want 0", len(got))
	}
}

// A snapshot is shared by every concurrent reader. If callers could write
// through a returned slice, one request could corrupt routing for all the
// others.
func TestReturnedSlicesDoNotAliasInternals(t *testing.T) {
	pools := []dex.Pool{
		v2("p1", tWETH, tUSDC, 1000, 2_000_000),
		v2("p2", tWETH, tDAI, 1000, 2_000_000),
	}
	s := BuildSnapshot(pools, time.Now())

	edges := s.EdgesFrom(tWETH.Address)
	edges[0].To = "0xcorrupted"

	again := s.EdgesFrom(tWETH.Address)
	if again[0].To == "0xcorrupted" {
		t.Error("EdgesFrom aliases internal storage")
	}

	got := s.Pools()
	got[0].Address = "0xcorrupted"
	if s.Pools()[0].Address == "0xcorrupted" {
		t.Error("Pools aliases internal storage")
	}
}

// The builder must not keep a pool whose state never loaded, because the
// router would spend work on an edge that can only ever return an error.
func TestBuildSnapshotSkipsUnusablePools(t *testing.T) {
	good := v2("good", tWETH, tUSDC, 1000, 2_000_000)

	noReserves := dex.Pool{
		Address: "bare", Protocol: dex.SushiswapV2,
		Token0: tWETH, Token1: tDAI, FeeBps: 30,
	}
	emptyReserves := v2("empty", tWETH, tWBTC, 0, 0)

	s := BuildSnapshot([]dex.Pool{good, noReserves, emptyReserves}, time.Now())

	if got := len(s.Pools()); got != 1 {
		t.Errorf("kept %d pools, want 1 usable", got)
	}
	if got := s.EdgeCount(); got != 2 {
		t.Errorf("EdgeCount() = %d, want 2", got)
	}
	if got := s.TokenCount(); got != 2 {
		t.Errorf("TokenCount() = %d, want 2 (unusable pools contribute no tokens)", got)
	}
}

func TestBuildSnapshotEmpty(t *testing.T) {
	s := BuildSnapshot(nil, time.Now())

	if s == nil {
		t.Fatal("BuildSnapshot(nil) = nil, want empty snapshot")
	}
	if s.TokenCount() != 0 || s.EdgeCount() != 0 {
		t.Errorf("empty snapshot has %d tokens and %d edges, want 0 and 0", s.TokenCount(), s.EdgeCount())
	}
	if got := s.EdgesFrom(tWETH.Address); len(got) != 0 {
		t.Errorf("EdgesFrom on empty snapshot = %d edges, want 0", len(got))
	}
}

func TestSnapshotTokenLookup(t *testing.T) {
	s := BuildSnapshot([]dex.Pool{v2("p1", tWETH, tUSDC, 1000, 2_000_000)}, time.Now())

	got, ok := s.Token(strings.ToUpper(tUSDC.Address))
	if !ok {
		t.Fatal("Token(USDC) not found")
	}
	if got.Symbol != "USDC" || got.Decimals != 6 {
		t.Errorf("Token(USDC) = %+v, want USDC/6", got)
	}
	if _, ok := s.Token("0xmissing"); ok {
		t.Error("Token(missing) found, want not found")
	}
}
