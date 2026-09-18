package amm

import (
	"errors"
	"math/big"
	"testing"
)

// singleRangePool builds a V3 pool at tick 0 (price 1) with liquidity L and
// initialised ticks placed far enough away that a modest swap never reaches
// them.
func singleRangePool(liquidity *big.Int, feeBps uint32) V3Pool {
	sqrtP, err := GetSqrtRatioAtTick(0)
	if err != nil {
		panic(err)
	}
	return V3Pool{
		SqrtPriceX96: sqrtP,
		Liquidity:    liquidity,
		Tick:         0,
		FeeBps:       feeBps,
		TickSpacing:  60,
		Ticks: []V3Tick{
			{Index: -60000, LiquidityNet: new(big.Int).Set(liquidity)},
			{Index: 60000, LiquidityNet: new(big.Int).Neg(liquidity)},
		},
	}
}

// Inside a single tick range a V3 pool is algebraically a constant product
// pool over virtual reserves x = L/sqrtP and y = L*sqrtP. If the V3 stepper is
// right, it must agree with the independent V2 implementation.
func TestSwapV3MatchesConstantProductWithinOneTick(t *testing.T) {
	liquidity := new(big.Int).Mul(big.NewInt(5_000_000), pow10(18))
	pool := singleRangePool(liquidity, 30)

	q96 := new(big.Int).Lsh(big.NewInt(1), 96)
	virtualX := new(big.Int).Div(new(big.Int).Mul(pool.Liquidity, q96), pool.SqrtPriceX96)
	virtualY := new(big.Int).Div(new(big.Int).Mul(pool.Liquidity, pool.SqrtPriceX96), q96)

	in := pow10(18)

	gotV3, err := SwapV3(in, pool, true)
	if err != nil {
		t.Fatalf("SwapV3 error = %v", err)
	}
	gotV2, err := SwapV2(in, V2Pool{ReserveIn: virtualX, ReserveOut: virtualY, FeeBps: 30})
	if err != nil {
		t.Fatalf("SwapV2 error = %v", err)
	}

	assertRelativelyClose(t, gotV3, gotV2, 1_000_000) // within 1 part per million
}

func assertRelativelyClose(t *testing.T, got, want *big.Int, oneOverTolerance int64) {
	t.Helper()

	diff := new(big.Int).Sub(got, want)
	diff.Abs(diff)

	allowed := new(big.Int).Div(new(big.Int).Abs(want), big.NewInt(oneOverTolerance))
	if allowed.Sign() == 0 {
		allowed = big.NewInt(1)
	}
	if diff.Cmp(allowed) > 0 {
		t.Errorf("got %s, want ~%s (diff %s exceeds allowance %s)", got, want, diff, allowed)
	}
}

func TestSwapV3ZeroAmount(t *testing.T) {
	pool := singleRangePool(new(big.Int).Mul(big.NewInt(1000), pow10(18)), 30)

	for _, in := range []*big.Int{big.NewInt(0), big.NewInt(-1)} {
		if _, err := SwapV3(in, pool, true); !errors.Is(err, ErrZeroAmount) {
			t.Errorf("SwapV3(%s) error = %v, want ErrZeroAmount", in, err)
		}
	}
}

func TestSwapV3ZeroLiquidity(t *testing.T) {
	pool := singleRangePool(big.NewInt(0), 30)
	if _, err := SwapV3(pow10(18), pool, true); !errors.Is(err, ErrInsufficientLiquidity) {
		t.Errorf("SwapV3 with zero liquidity error = %v, want ErrInsufficientLiquidity", err)
	}
}

// Crossing a tick moves into a range with less liquidity, so the marginal rate
// gets worse. Output must come in strictly below what the first range would
// have produced if it extended forever.
func TestSwapV3CrossingTickReducesOutput(t *testing.T) {
	liquidity := new(big.Int).Mul(big.NewInt(1000), pow10(18))
	sqrtP, err := GetSqrtRatioAtTick(0)
	if err != nil {
		t.Fatal(err)
	}

	// A tick just below the current price that removes 90% of the liquidity
	// when crossed downward.
	removed := new(big.Int).Div(new(big.Int).Mul(liquidity, big.NewInt(9)), big.NewInt(10))

	narrow := V3Pool{
		SqrtPriceX96: sqrtP,
		Liquidity:    liquidity,
		Tick:         0,
		FeeBps:       30,
		TickSpacing:  60,
		Ticks: []V3Tick{
			{Index: -180000, LiquidityNet: new(big.Int).Sub(liquidity, removed)},
			{Index: -60, LiquidityNet: removed},
			{Index: 60000, LiquidityNet: new(big.Int).Neg(liquidity)},
		},
	}

	wide := singleRangePool(liquidity, 30)

	// Large enough to push the price through the -60 tick.
	in := new(big.Int).Mul(big.NewInt(50), pow10(18))

	gotNarrow, err := SwapV3(in, narrow, true)
	if err != nil {
		t.Fatalf("SwapV3 narrow error = %v", err)
	}
	gotWide, err := SwapV3(in, wide, true)
	if err != nil {
		t.Fatalf("SwapV3 wide error = %v", err)
	}

	if gotNarrow.Cmp(gotWide) >= 0 {
		t.Errorf("output after crossing into thin liquidity = %s, want strictly less than deep-pool output %s", gotNarrow, gotWide)
	}
	if gotNarrow.Sign() <= 0 {
		t.Errorf("output = %s, want positive", gotNarrow)
	}
}

func TestSwapV3OutputIsConcave(t *testing.T) {
	pool := singleRangePool(new(big.Int).Mul(big.NewInt(10_000), pow10(18)), 30)

	x := pow10(18)
	twoX := new(big.Int).Mul(x, big.NewInt(2))

	fx, err := SwapV3(x, pool, true)
	if err != nil {
		t.Fatalf("SwapV3(x) error = %v", err)
	}
	f2x, err := SwapV3(twoX, pool, true)
	if err != nil {
		t.Fatalf("SwapV3(2x) error = %v", err)
	}

	if f2x.Cmp(fx) <= 0 {
		t.Errorf("f(2x) = %s, want greater than f(x) = %s", f2x, fx)
	}
	twice := new(big.Int).Mul(fx, big.NewInt(2))
	if f2x.Cmp(twice) >= 0 {
		t.Errorf("f(2x) = %s, want strictly less than 2*f(x) = %s", f2x, twice)
	}
}

func TestSwapV3BothDirections(t *testing.T) {
	pool := singleRangePool(new(big.Int).Mul(big.NewInt(100_000), pow10(18)), 30)
	in := pow10(18)

	zeroForOne, err := SwapV3(in, pool, true)
	if err != nil {
		t.Fatalf("SwapV3 zeroForOne error = %v", err)
	}
	oneForZero, err := SwapV3(in, pool, false)
	if err != nil {
		t.Fatalf("SwapV3 oneForZero error = %v", err)
	}

	// At tick 0 the pool is symmetric, so both directions quote the same.
	assertRelativelyClose(t, oneForZero, zeroForOne, 1_000_000)
}

// A trade too large for the supplied tick window cannot be quoted honestly, so
// it must error rather than silently report a partial fill as a full one.
func TestSwapV3ExhaustedTickWindowErrors(t *testing.T) {
	liquidity := new(big.Int).Mul(big.NewInt(10), pow10(18))
	sqrtP, err := GetSqrtRatioAtTick(0)
	if err != nil {
		t.Fatal(err)
	}

	pool := V3Pool{
		SqrtPriceX96: sqrtP,
		Liquidity:    liquidity,
		Tick:         0,
		FeeBps:       30,
		TickSpacing:  60,
		// Only one tick below the current price. Beyond it the window ends.
		Ticks: []V3Tick{
			{Index: -60, LiquidityNet: new(big.Int).Set(liquidity)},
		},
	}

	huge := new(big.Int).Mul(big.NewInt(1_000_000), pow10(18))
	if _, err := SwapV3(huge, pool, true); !errors.Is(err, ErrInsufficientLiquidity) {
		t.Errorf("SwapV3 beyond tick window error = %v, want ErrInsufficientLiquidity", err)
	}
}

func TestSwapV3DoesNotMutateInputs(t *testing.T) {
	pool := singleRangePool(new(big.Int).Mul(big.NewInt(1000), pow10(18)), 30)

	sqrtCopy := new(big.Int).Set(pool.SqrtPriceX96)
	liqCopy := new(big.Int).Set(pool.Liquidity)
	tickNetCopy := new(big.Int).Set(pool.Ticks[0].LiquidityNet)
	in := pow10(18)
	inCopy := new(big.Int).Set(in)

	if _, err := SwapV3(in, pool, true); err != nil {
		t.Fatalf("SwapV3 error = %v", err)
	}

	if pool.SqrtPriceX96.Cmp(sqrtCopy) != 0 {
		t.Errorf("SqrtPriceX96 mutated: %s, want %s", pool.SqrtPriceX96, sqrtCopy)
	}
	if pool.Liquidity.Cmp(liqCopy) != 0 {
		t.Errorf("Liquidity mutated: %s, want %s", pool.Liquidity, liqCopy)
	}
	if pool.Ticks[0].LiquidityNet.Cmp(tickNetCopy) != 0 {
		t.Errorf("tick LiquidityNet mutated: %s, want %s", pool.Ticks[0].LiquidityNet, tickNetCopy)
	}
	if in.Cmp(inCopy) != 0 {
		t.Errorf("input mutated: %s, want %s", in, inCopy)
	}
}

func TestSwapV3HigherFeeGivesLessOutput(t *testing.T) {
	liquidity := new(big.Int).Mul(big.NewInt(100_000), pow10(18))

	cheap := singleRangePool(liquidity, 5)
	dear := singleRangePool(liquidity, 100)

	in := pow10(18)

	a, err := SwapV3(in, cheap, true)
	if err != nil {
		t.Fatalf("SwapV3 cheap error = %v", err)
	}
	b, err := SwapV3(in, dear, true)
	if err != nil {
		t.Fatalf("SwapV3 dear error = %v", err)
	}
	if b.Cmp(a) >= 0 {
		t.Errorf("100bps output %s, want less than 5bps output %s", b, a)
	}
}
