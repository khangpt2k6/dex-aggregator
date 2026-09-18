package amm

import (
	"errors"
	"math/big"
	"testing"
)

func TestGetSqrtRatioAtTickZero(t *testing.T) {
	got, err := GetSqrtRatioAtTick(0)
	if err != nil {
		t.Fatalf("GetSqrtRatioAtTick(0) error = %v", err)
	}
	// Tick 0 is price 1, so sqrt(1) in Q64.96 is exactly 2^96.
	want := new(big.Int).Lsh(big.NewInt(1), 96)
	if got.Cmp(want) != 0 {
		t.Errorf("GetSqrtRatioAtTick(0) = %s, want %s", got, want)
	}
}

func TestGetSqrtRatioAtTickBounds(t *testing.T) {
	lo, err := GetSqrtRatioAtTick(MinTick)
	if err != nil {
		t.Fatalf("GetSqrtRatioAtTick(MinTick) error = %v", err)
	}
	if lo.Cmp(MinSqrtRatio()) != 0 {
		t.Errorf("ratio at MinTick = %s, want %s", lo, MinSqrtRatio())
	}

	hi, err := GetSqrtRatioAtTick(MaxTick)
	if err != nil {
		t.Fatalf("GetSqrtRatioAtTick(MaxTick) error = %v", err)
	}
	// MaxSqrtRatio is defined as the ratio at MaxTick, so this is equality,
	// not a strict bound. Swaps are what must stay strictly inside it.
	if hi.Cmp(MaxSqrtRatio()) != 0 {
		t.Errorf("ratio at MaxTick = %s, want %s", hi, MaxSqrtRatio())
	}
}

func TestGetSqrtRatioAtTickRejectsOutOfRange(t *testing.T) {
	for _, tick := range []int32{MinTick - 1, MaxTick + 1, -1_000_000, 1_000_000} {
		if _, err := GetSqrtRatioAtTick(tick); !errors.Is(err, ErrTickOutOfRange) {
			t.Errorf("GetSqrtRatioAtTick(%d) error = %v, want ErrTickOutOfRange", tick, err)
		}
	}
}

func TestGetSqrtRatioAtTickIsMonotonic(t *testing.T) {
	ticks := []int32{-887272, -500000, -100000, -10000, -60, -1, 0, 1, 60, 10000, 100000, 500000, 887271}

	var prev *big.Int
	for _, tick := range ticks {
		got, err := GetSqrtRatioAtTick(tick)
		if err != nil {
			t.Fatalf("GetSqrtRatioAtTick(%d) error = %v", tick, err)
		}
		if prev != nil && got.Cmp(prev) <= 0 {
			t.Errorf("ratio at tick %d = %s, want greater than previous %s", tick, got, prev)
		}
		prev = got
	}
}

// Tick i is price 1.0001^i, so the ratio at tick i and at tick -i must be
// reciprocal. Checking that round trip catches sign errors in the
// bit-decomposition table.
func TestGetSqrtRatioAtTickIsReciprocalAcrossZero(t *testing.T) {
	q96 := new(big.Int).Lsh(big.NewInt(1), 96)

	for _, tick := range []int32{1, 60, 1000, 50000, 200000} {
		pos, err := GetSqrtRatioAtTick(tick)
		if err != nil {
			t.Fatalf("tick %d: %v", tick, err)
		}
		neg, err := GetSqrtRatioAtTick(-tick)
		if err != nil {
			t.Fatalf("tick %d: %v", -tick, err)
		}

		// pos * neg should be 2^192, within rounding.
		product := new(big.Int).Mul(pos, neg)
		want := new(big.Int).Mul(q96, q96)

		diff := new(big.Int).Sub(product, want)
		diff.Abs(diff)

		// Allow a relative error of 1e-9.
		tolerance := new(big.Int).Div(want, big.NewInt(1_000_000_000))
		if diff.Cmp(tolerance) > 0 {
			t.Errorf("tick %d: ratio(%d)*ratio(%d) = %s, want ~%s (diff %s)", tick, tick, -tick, product, want, diff)
		}
	}
}
