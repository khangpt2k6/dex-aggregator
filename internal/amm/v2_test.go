package amm

import (
	"errors"
	"math/big"
	"testing"
)

func bi(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big.Int literal: " + s)
	}
	return n
}

// pow10 returns 10^n, used to write reserve amounts at token decimals.
func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

func TestSwapV2KnownValue(t *testing.T) {
	// 1000 WETH against 2,000,000 USDC, the canonical 0.3% fee.
	// amountInWithFee = 1e18 * 9970 = 9.97e21
	// numerator       = 9.97e21 * 2e12
	// denominator     = 1000e18 * 10000 + 9.97e21
	// => 1994.011964... USDC, truncated by integer division.
	pool := V2Pool{
		ReserveIn:  new(big.Int).Mul(big.NewInt(1000), pow10(18)),
		ReserveOut: new(big.Int).Mul(big.NewInt(2_000_000), pow10(6)),
		FeeBps:     30,
	}
	in := pow10(18)

	got, err := SwapV2(in, pool)
	if err != nil {
		t.Fatalf("SwapV2 error = %v", err)
	}

	want := expectedV2(in, pool)
	if got.Cmp(want) != 0 {
		t.Errorf("SwapV2 = %s, want %s", got, want)
	}

	// Sanity on magnitude: just under 2000 USDC (6 decimals).
	lo, hi := bi("1990000000"), bi("2000000000")
	if got.Cmp(lo) < 0 || got.Cmp(hi) >= 0 {
		t.Errorf("SwapV2 = %s, want between %s and %s", got, lo, hi)
	}
}

// expectedV2 is the reference formula, written out independently of the
// implementation so the test does not simply restate the code under test.
func expectedV2(in *big.Int, p V2Pool) *big.Int {
	feeMul := big.NewInt(int64(10000 - p.FeeBps))
	inWithFee := new(big.Int).Mul(in, feeMul)
	num := new(big.Int).Mul(inWithFee, p.ReserveOut)
	den := new(big.Int).Add(new(big.Int).Mul(p.ReserveIn, big.NewInt(10000)), inWithFee)
	return new(big.Int).Div(num, den)
}

func TestSwapV2ZeroAmount(t *testing.T) {
	pool := V2Pool{ReserveIn: pow10(21), ReserveOut: pow10(21), FeeBps: 30}

	for _, in := range []*big.Int{big.NewInt(0), big.NewInt(-5)} {
		if _, err := SwapV2(in, pool); !errors.Is(err, ErrZeroAmount) {
			t.Errorf("SwapV2(%s) error = %v, want ErrZeroAmount", in, err)
		}
	}
}

func TestSwapV2EmptyReserves(t *testing.T) {
	cases := []V2Pool{
		{ReserveIn: big.NewInt(0), ReserveOut: pow10(21), FeeBps: 30},
		{ReserveIn: pow10(21), ReserveOut: big.NewInt(0), FeeBps: 30},
	}
	for _, p := range cases {
		if _, err := SwapV2(pow10(18), p); !errors.Is(err, ErrInsufficientLiquidity) {
			t.Errorf("SwapV2 with empty reserve error = %v, want ErrInsufficientLiquidity", err)
		}
	}
}

// A constant product pool can never be drained: output is strictly bounded by
// the output reserve no matter how large the input.
func TestSwapV2CannotDrainPool(t *testing.T) {
	pool := V2Pool{ReserveIn: pow10(21), ReserveOut: pow10(21), FeeBps: 30}
	huge := new(big.Int).Mul(pow10(21), big.NewInt(1_000_000))

	got, err := SwapV2(huge, pool)
	if err != nil {
		t.Fatalf("SwapV2 error = %v", err)
	}
	if got.Cmp(pool.ReserveOut) >= 0 {
		t.Errorf("SwapV2 = %s, want strictly less than reserveOut %s", got, pool.ReserveOut)
	}
}

// Concavity is the property that makes spot-price routing wrong. If output
// were linear in input, a single best-rate pool would always win and the
// amount-aware router would be pointless. Assert the curve really does bend.
func TestSwapV2OutputIsConcave(t *testing.T) {
	pool := V2Pool{
		ReserveIn:  new(big.Int).Mul(big.NewInt(100), pow10(18)),
		ReserveOut: new(big.Int).Mul(big.NewInt(100), pow10(18)),
		FeeBps:     30,
	}

	x := pow10(18)
	twoX := new(big.Int).Mul(x, big.NewInt(2))

	fx, err := SwapV2(x, pool)
	if err != nil {
		t.Fatalf("SwapV2(x) error = %v", err)
	}
	f2x, err := SwapV2(twoX, pool)
	if err != nil {
		t.Fatalf("SwapV2(2x) error = %v", err)
	}

	twiceFx := new(big.Int).Mul(fx, big.NewInt(2))
	if f2x.Cmp(twiceFx) >= 0 {
		t.Errorf("f(2x) = %s, want strictly less than 2*f(x) = %s", f2x, twiceFx)
	}
	// Still monotonic increasing.
	if f2x.Cmp(fx) <= 0 {
		t.Errorf("f(2x) = %s, want greater than f(x) = %s", f2x, fx)
	}
}

func TestSwapV2FeeReducesOutput(t *testing.T) {
	base := V2Pool{ReserveIn: pow10(21), ReserveOut: pow10(21)}

	noFee := base
	noFee.FeeBps = 0
	withFee := base
	withFee.FeeBps = 30

	a, err := SwapV2(pow10(18), noFee)
	if err != nil {
		t.Fatalf("SwapV2 no fee error = %v", err)
	}
	b, err := SwapV2(pow10(18), withFee)
	if err != nil {
		t.Fatalf("SwapV2 with fee error = %v", err)
	}
	if b.Cmp(a) >= 0 {
		t.Errorf("fee output %s, want less than no-fee output %s", b, a)
	}
}

func TestSwapV2RejectsAbsurdFee(t *testing.T) {
	pool := V2Pool{ReserveIn: pow10(21), ReserveOut: pow10(21), FeeBps: 10000}
	if _, err := SwapV2(pow10(18), pool); err == nil {
		t.Error("SwapV2 with 100% fee returned nil error, want error")
	}
}

// The implementation must not mutate its inputs. The router calls this in a
// hot loop with pooled values; silent mutation would corrupt the snapshot.
func TestSwapV2DoesNotMutateInputs(t *testing.T) {
	pool := V2Pool{ReserveIn: pow10(21), ReserveOut: pow10(21), FeeBps: 30}
	in := pow10(18)

	inCopy := new(big.Int).Set(in)
	rInCopy := new(big.Int).Set(pool.ReserveIn)
	rOutCopy := new(big.Int).Set(pool.ReserveOut)

	if _, err := SwapV2(in, pool); err != nil {
		t.Fatalf("SwapV2 error = %v", err)
	}

	if in.Cmp(inCopy) != 0 {
		t.Errorf("input mutated: %s, want %s", in, inCopy)
	}
	if pool.ReserveIn.Cmp(rInCopy) != 0 {
		t.Errorf("ReserveIn mutated: %s, want %s", pool.ReserveIn, rInCopy)
	}
	if pool.ReserveOut.Cmp(rOutCopy) != 0 {
		t.Errorf("ReserveOut mutated: %s, want %s", pool.ReserveOut, rOutCopy)
	}
}
