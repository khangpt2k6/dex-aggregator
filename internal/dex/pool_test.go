package dex

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

var (
	weth = Token{Address: "0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2", Symbol: "WETH", Decimals: 18}
	usdc = Token{Address: "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48", Symbol: "USDC", Decimals: 6}
)

func v2Pool() Pool {
	return Pool{
		Address:  "0xpool2",
		Protocol: SushiswapV2,
		Token0:   weth,
		Token1:   usdc,
		FeeBps:   30,
		Reserve0: new(big.Int).Mul(big.NewInt(1000), pow10(18)),
		Reserve1: new(big.Int).Mul(big.NewInt(2_000_000), pow10(6)),
	}
}

func v3Pool() Pool {
	sqrtP, err := amm.GetSqrtRatioAtTick(0)
	if err != nil {
		panic(err)
	}
	liq := new(big.Int).Mul(big.NewInt(1_000_000), pow10(18))
	return Pool{
		Address:      "0xpool3",
		Protocol:     UniswapV3,
		Token0:       weth,
		Token1:       usdc,
		FeeBps:       30,
		SqrtPriceX96: sqrtP,
		Liquidity:    liq,
		Tick:         0,
		TickSpacing:  60,
		Ticks: []amm.V3Tick{
			{Index: -60000, LiquidityNet: new(big.Int).Set(liq)},
			{Index: 60000, LiquidityNet: new(big.Int).Neg(liq)},
		},
	}
}

func TestPoolAmountOutV2BothDirections(t *testing.T) {
	p := v2Pool()

	out0, err := p.AmountOut(pow10(18), weth.Address)
	if err != nil {
		t.Fatalf("AmountOut token0 in: %v", err)
	}
	if out0.Sign() <= 0 {
		t.Errorf("out = %s, want positive", out0)
	}

	out1, err := p.AmountOut(new(big.Int).Mul(big.NewInt(2000), pow10(6)), usdc.Address)
	if err != nil {
		t.Fatalf("AmountOut token1 in: %v", err)
	}
	if out1.Sign() <= 0 {
		t.Errorf("out = %s, want positive", out1)
	}

	// Selling 1 WETH into a 1000/2,000,000 pool yields roughly 2000 USDC
	// (6 decimals), and selling 2000 USDC yields roughly 1 WETH.
	if out0.Cmp(big.NewInt(1_900_000_000)) < 0 || out0.Cmp(big.NewInt(2_000_000_000)) > 0 {
		t.Errorf("WETH->USDC out = %s, want near 2000e6", out0)
	}
	if out1.Cmp(new(big.Int).Div(pow10(18), big.NewInt(2))) < 0 {
		t.Errorf("USDC->WETH out = %s, want near 1e18", out1)
	}
}

func TestPoolAmountOutV3Dispatches(t *testing.T) {
	p := v3Pool()

	out, err := p.AmountOut(pow10(18), weth.Address)
	if err != nil {
		t.Fatalf("AmountOut: %v", err)
	}
	if out.Sign() <= 0 {
		t.Errorf("out = %s, want positive", out)
	}
}

func TestPoolAmountOutUnknownToken(t *testing.T) {
	p := v2Pool()
	_, err := p.AmountOut(pow10(18), "0xdeadbeef")
	if !errors.Is(err, ErrTokenNotInPool) {
		t.Errorf("error = %v, want ErrTokenNotInPool", err)
	}
}

// Addresses arrive from RPC and from user input with inconsistent casing.
// Matching must not depend on it.
func TestPoolAmountOutIsCaseInsensitive(t *testing.T) {
	p := v2Pool()

	lower, err := p.AmountOut(pow10(18), strings.ToLower(weth.Address))
	if err != nil {
		t.Fatalf("lowercase address: %v", err)
	}
	upper, err := p.AmountOut(pow10(18), strings.ToUpper(weth.Address))
	if err != nil {
		t.Fatalf("uppercase address: %v", err)
	}
	if lower.Cmp(upper) != 0 {
		t.Errorf("lowercase out %s != uppercase out %s", lower, upper)
	}
}

func TestPoolOtherToken(t *testing.T) {
	p := v2Pool()

	got, ok := p.OtherToken(weth.Address)
	if !ok || got.Address != usdc.Address {
		t.Errorf("OtherToken(WETH) = %v, %v; want USDC, true", got.Symbol, ok)
	}
	if _, ok := p.OtherToken("0xnope"); ok {
		t.Error("OtherToken on foreign address returned ok, want false")
	}
}

func TestPoolMissingStateErrors(t *testing.T) {
	// A V2 pool with no reserves loaded yet.
	bare := Pool{Protocol: SushiswapV2, Token0: weth, Token1: usdc, FeeBps: 30}
	if _, err := bare.AmountOut(pow10(18), weth.Address); err == nil {
		t.Error("V2 pool with nil reserves returned nil error, want error")
	}

	bareV3 := Pool{Protocol: UniswapV3, Token0: weth, Token1: usdc, FeeBps: 30}
	if _, err := bareV3.AmountOut(pow10(18), weth.Address); err == nil {
		t.Error("V3 pool with nil state returned nil error, want error")
	}
}

func TestTokenRegistry(t *testing.T) {
	tokens := Tokens()
	if len(tokens) < 6 {
		t.Fatalf("Tokens() returned %d tokens, want at least 6", len(tokens))
	}

	seen := map[string]bool{}
	for _, tok := range tokens {
		if tok.Address == "" || tok.Symbol == "" {
			t.Errorf("token %+v has empty field", tok)
		}
		if tok.Decimals == 0 || tok.Decimals > 18 {
			t.Errorf("token %s has implausible decimals %d", tok.Symbol, tok.Decimals)
		}
		key := strings.ToLower(tok.Address)
		if seen[key] {
			t.Errorf("duplicate token address %s", tok.Address)
		}
		seen[key] = true
	}

	if _, ok := TokenBySymbol("WETH"); !ok {
		t.Error("TokenBySymbol(WETH) not found")
	}
	if _, ok := TokenBySymbol("weth"); !ok {
		t.Error("TokenBySymbol is case sensitive, want case insensitive")
	}
	if _, ok := TokenBySymbol("NOTATOKEN"); ok {
		t.Error("TokenBySymbol(NOTATOKEN) found, want not found")
	}

	if _, ok := TokenByAddress(strings.ToLower(weth.Address)); !ok {
		t.Error("TokenByAddress(weth, lowercased) not found")
	}
}

// Tokens() must not hand out a slice that callers can use to corrupt the
// registry for everyone else.
func TestTokensReturnsCopy(t *testing.T) {
	first := Tokens()
	original := first[0].Symbol
	first[0].Symbol = "CORRUPTED"

	second := Tokens()
	if second[0].Symbol != original {
		t.Errorf("registry mutated through returned slice: %s", second[0].Symbol)
	}
}
