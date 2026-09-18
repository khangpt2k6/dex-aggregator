package onchain

import (
	"math/big"
	"strings"
	"testing"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/rpc"
)

func mustToken(t *testing.T, symbol string) dex.Token {
	t.Helper()
	tok, ok := dex.TokenBySymbol(symbol)
	if !ok {
		t.Fatalf("%s not in registry", symbol)
	}
	return tok
}

// These are real mainnet pool addresses. Checking the derivation against them
// is the only way to know the salt layout and init code hash are right: a
// wrong derivation produces a perfectly well-formed address that simply holds
// nothing, and every probe would come back reverted with no indication why.
func TestV3PoolAddressMatchesKnownMainnetPools(t *testing.T) {
	weth := mustToken(t, "WETH")
	usdc := mustToken(t, "USDC")
	dai := mustToken(t, "DAI")
	wbtc := mustToken(t, "WBTC")

	cases := []struct {
		name   string
		a, b   dex.Token
		feeBps uint32
		want   string
	}{
		{"USDC/WETH 5 bps", usdc, weth, 5, "0x88e6A0c2dDD26FEEb64F039a2c41296FcB3f5640"},
		{"USDC/WETH 30 bps", usdc, weth, 30, "0x8ad599c3A0ff1De082011EFDDc58f1908eb6e6D8"},
		{"DAI/USDC 1 bps", dai, usdc, 1, "0x5777d92f208679DB4b9778590Fa3CAB3aC9e2168"},
		{"WBTC/WETH 30 bps", wbtc, weth, 30, "0xCBCdF9626bC03E24f779434178A73a0B4bad62eD"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := v3PoolAddress(c.a, c.b, c.feeBps)
			if err != nil {
				t.Fatalf("v3PoolAddress: %v", err)
			}
			if !strings.EqualFold(got, c.want) {
				t.Errorf("derived %s, want %s", got, c.want)
			}
		})
	}
}

func TestV2PairAddressMatchesKnownMainnetPair(t *testing.T) {
	weth := mustToken(t, "WETH")
	usdc := mustToken(t, "USDC")

	// The Sushiswap USDC/WETH pair on mainnet.
	want := "0x397FF1542f962076d0BFE58eA045FfA2d347ACa0"

	got, err := v2PairAddress(usdc, weth)
	if err != nil {
		t.Fatalf("v2PairAddress: %v", err)
	}
	if !strings.EqualFold(got, want) {
		t.Errorf("derived %s, want %s", got, want)
	}
}

// Both factories sort the pair by address, and the salt depends on that order.
// Passing the tokens the other way round must derive the same pool.
func TestPoolAddressIsOrderIndependent(t *testing.T) {
	weth := mustToken(t, "WETH")
	usdc := mustToken(t, "USDC")

	a, err := v3PoolAddress(weth, usdc, 30)
	if err != nil {
		t.Fatal(err)
	}
	b, err := v3PoolAddress(usdc, weth, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(a, b) {
		t.Errorf("v3 derivation depends on argument order: %s vs %s", a, b)
	}

	c, err := v2PairAddress(weth, usdc)
	if err != nil {
		t.Fatal(err)
	}
	d, err := v2PairAddress(usdc, weth)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(c, d) {
		t.Errorf("v2 derivation depends on argument order: %s vs %s", c, d)
	}
}

// Different fee tiers are different pools, or the source would probe the same
// address three times and report one pool as three.
func TestV3FeeTiersDeriveDistinctPools(t *testing.T) {
	weth := mustToken(t, "WETH")
	usdc := mustToken(t, "USDC")

	seen := map[string]uint32{}
	for _, fee := range DefaultV3FeeTiers {
		addr, err := v3PoolAddress(weth, usdc, fee)
		if err != nil {
			t.Fatal(err)
		}
		key := strings.ToLower(addr)
		if prev, ok := seen[key]; ok {
			t.Errorf("fee tiers %d and %d derive the same address %s", prev, fee, addr)
		}
		seen[key] = fee
	}
}

func TestPairsOfIsCompleteAndStable(t *testing.T) {
	tokens := dex.Tokens()
	pairs := pairsOf(tokens)

	want := len(tokens) * (len(tokens) - 1) / 2
	if len(pairs) != want {
		t.Errorf("got %d pairs from %d tokens, want %d", len(pairs), len(tokens), want)
	}

	// Stable ordering keeps a refresh issuing calls in the same sequence, which
	// makes a failing batch reproducible.
	again := pairsOf(tokens)
	for i := range pairs {
		if pairs[i][0].Address != again[i][0].Address || pairs[i][1].Address != again[i][1].Address {
			t.Fatalf("pair %d differs between calls", i)
		}
	}

	// No self-pairs, no duplicates.
	seen := map[string]bool{}
	for _, p := range pairs {
		if strings.EqualFold(p[0].Address, p[1].Address) {
			t.Errorf("self-pair for %s", p[0].Symbol)
		}
		key := strings.ToLower(p[0].Address + p[1].Address)
		if seen[key] {
			t.Errorf("duplicate pair %s/%s", p[0].Symbol, p[1].Symbol)
		}
		seen[key] = true
	}
}

func TestDecodeSlot0(t *testing.T) {
	sqrtPrice, _ := new(big.Int).SetString("1417910117290349850000000000", 10)
	tick := int32(-201600)

	data := make([]byte, 0, 7*32)
	data = append(data, leftPad32(sqrtPrice.Bytes())...)

	encTick, err := encodeInt24(tick)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, encTick...)
	data = append(data, make([]byte, 5*32)...)

	gotPrice, gotTick, err := decodeSlot0(data)
	if err != nil {
		t.Fatalf("decodeSlot0: %v", err)
	}
	if gotPrice.Cmp(sqrtPrice) != 0 {
		t.Errorf("sqrtPriceX96 = %s, want %s", gotPrice, sqrtPrice)
	}
	if gotTick != tick {
		t.Errorf("tick = %d, want %d", gotTick, tick)
	}
}

func TestDecodeSlot0ShortData(t *testing.T) {
	if _, _, err := decodeSlot0(make([]byte, 16)); err == nil {
		t.Error("decodeSlot0 on short data returned nil error, want error")
	}
}

func TestInt24RoundTrip(t *testing.T) {
	for _, tick := range []int32{0, 1, -1, 60, -60, 201600, -201600, amm.MinTick, amm.MaxTick} {
		enc, err := encodeInt24(tick)
		if err != nil {
			t.Fatalf("encodeInt24(%d): %v", tick, err)
		}
		got, err := decodeInt24(enc)
		if err != nil {
			t.Fatalf("decodeInt24(%d): %v", tick, err)
		}
		if got != tick {
			t.Errorf("round trip of %d gave %d", tick, got)
		}
	}
}

func TestEncodeInt24RejectsOutOfRange(t *testing.T) {
	if _, err := encodeInt24(amm.MaxTick + 1); err == nil {
		t.Error("encodeInt24 past MaxTick returned nil error, want error")
	}
}

// liquidityNet is int128 and is negative at the upper bound of every position.
// Reading it unsigned would add liquidity where the pool removes it.
func TestDecodeTickHandlesNegativeLiquidityNet(t *testing.T) {
	net, _ := new(big.Int).SetString("-123456789012345678901", 10)

	encoded := new(big.Int).Add(net, new(big.Int).Lsh(big.NewInt(1), 128))

	data := make([]byte, 8*32)
	copy(data[32:64], leftPad32(encoded.Bytes()))
	data[8*32-1] = 1 // initialized

	got, initialized, err := decodeTick(data)
	if err != nil {
		t.Fatalf("decodeTick: %v", err)
	}
	if !initialized {
		t.Error("initialized = false, want true")
	}
	if got.Cmp(net) != 0 {
		t.Errorf("liquidityNet = %s, want %s", got, net)
	}
}

func TestDecodeTickReportsUninitialized(t *testing.T) {
	data := make([]byte, 8*32)
	copy(data[32:64], leftPad32(big.NewInt(500).Bytes()))
	// initialized flag left at zero.

	_, initialized, err := decodeTick(data)
	if err != nil {
		t.Fatalf("decodeTick: %v", err)
	}
	if initialized {
		t.Error("initialized = true for a zeroed flag, want false")
	}
}

func TestDecodeReservesReadsBothSides(t *testing.T) {
	r0 := new(big.Int).Mul(big.NewInt(1234), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
	r1 := new(big.Int).Mul(big.NewInt(5678), new(big.Int).Exp(big.NewInt(10), big.NewInt(6), nil))

	data := make([]byte, 0, 3*32)
	data = append(data, leftPad32(r0.Bytes())...)
	data = append(data, leftPad32(r1.Bytes())...)
	data = append(data, leftPad32(big.NewInt(1758000000).Bytes())...)

	got0, got1, err := decodeReserves(data)
	if err != nil {
		t.Fatalf("decodeReserves: %v", err)
	}
	if got0.Cmp(r0) != 0 || got1.Cmp(r1) != 0 {
		t.Errorf("reserves = %s/%s, want %s/%s", got0, got1, r0, r1)
	}
}

func TestWindowTicksSnapsToSpacingAndCoversBothSides(t *testing.T) {
	ticks := windowTicks(-201605, 60)

	if len(ticks) != tickWindow*2 {
		t.Fatalf("got %d ticks, want %d", len(ticks), tickWindow*2)
	}

	below, above := 0, 0
	for _, idx := range ticks {
		if idx%60 != 0 {
			t.Errorf("tick %d is not a multiple of the spacing", idx)
		}
		if idx < -201605 {
			below++
		} else {
			above++
		}
		if idx < amm.MinTick || idx > amm.MaxTick {
			t.Errorf("tick %d is outside protocol bounds", idx)
		}
	}
	if below == 0 || above == 0 {
		t.Errorf("window has %d below and %d above, want both sides", below, above)
	}

	// Ascending, which is what the swap math expects.
	for i := 1; i < len(ticks); i++ {
		if ticks[i] <= ticks[i-1] {
			t.Errorf("ticks are not ascending at %d: %d then %d", i, ticks[i-1], ticks[i])
		}
	}
}

func TestWindowTicksClampsNearProtocolBounds(t *testing.T) {
	for _, idx := range windowTicks(amm.MaxTick-120, 60) {
		if idx > amm.MaxTick {
			t.Errorf("tick %d exceeds MaxTick", idx)
		}
	}
	for _, idx := range windowTicks(amm.MinTick+120, 60) {
		if idx < amm.MinTick {
			t.Errorf("tick %d is below MinTick", idx)
		}
	}
}

func TestSourcesSatisfyPoolSourceInterface(t *testing.T) {
	mc := rpc.NewMulticaller(nil, "", 0)
	var _ dex.PoolSource = NewUniswapV3(mc, dex.Tokens(), nil)
	var _ dex.PoolSource = NewSushiswapV2(mc, dex.Tokens())
}

func TestSourceNames(t *testing.T) {
	mc := rpc.NewMulticaller(nil, "", 0)
	if got := NewUniswapV3(mc, nil, nil).Name(); got != "uniswap-v3" {
		t.Errorf("Name() = %q", got)
	}
	if got := NewSushiswapV2(mc, nil).Name(); got != "sushiswap-v2" {
		t.Errorf("Name() = %q", got)
	}
}
