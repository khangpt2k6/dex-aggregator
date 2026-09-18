package amm

import (
	"math/big"
	"sort"
)

// V3Tick is one initialised tick boundary.
//
// LiquidityNet is the change in active liquidity when the tick is crossed
// moving upward in price. Crossing downward applies the negation.
type V3Tick struct {
	Index        int32
	LiquidityNet *big.Int

	// SqrtRatioX96 caches GetSqrtRatioAtTick(Index).
	//
	// Deriving it costs twenty big.Int multiplications, and the router asks
	// for it on every tick of every candidate pool on every relaxation round.
	// Tick indices do not change between snapshots, so the work belongs at
	// build time. Leave it nil and the swap computes it on demand.
	SqrtRatioX96 *big.Int
}

// PrepareTicks returns a copy of ticks sorted by index with every sqrt ratio
// precomputed, ready to be quoted against without per-call setup.
//
// Callers that build pool state once and quote against it many times should
// run this at build time. The returned slice is independent of the input, so
// the result is safe to share across goroutines.
func PrepareTicks(ticks []V3Tick) ([]V3Tick, error) {
	if len(ticks) == 0 {
		return nil, nil
	}

	out := make([]V3Tick, len(ticks))
	copy(out, ticks)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })

	for i := range out {
		if out[i].SqrtRatioX96 != nil {
			continue
		}
		ratio, err := GetSqrtRatioAtTick(out[i].Index)
		if err != nil {
			return nil, err
		}
		out[i].SqrtRatioX96 = ratio
	}
	return out, nil
}

// V3Pool is the concentrated liquidity state needed to quote a swap.
//
// Ticks holds the initialised ticks we fetched. It is a window, not the whole
// tick map: reading every tick of a mainnet pool would cost more RPC calls
// than the quote is worth. A swap that runs past the end of the window returns
// ErrInsufficientLiquidity rather than extrapolating, because a quote that
// assumed liquidity we never observed would be a guess presented as a number.
type V3Pool struct {
	SqrtPriceX96 *big.Int
	Liquidity    *big.Int
	Tick         int32
	FeeBps       uint32
	TickSpacing  int32
	Ticks        []V3Tick
}

// feePips converts the basis-point fee to the hundredths-of-a-bip scale the
// swap math uses. The Uniswap fee tiers (1, 5, 30 and 100 bps) are all whole
// basis points, so nothing is lost.
func (p V3Pool) feePips() int64 { return int64(p.FeeBps) * 100 }

const pipDenominator = 1_000_000

// SwapV3 returns the output amount for an exact-input swap against a
// concentrated liquidity pool.
//
// See SwapV3Into for the algorithm. This form allocates its own workspace and
// is the right choice for one-off calls; anything in a loop should reuse a
// Scratch instead.
func SwapV3(in *big.Int, p V3Pool, zeroForOne bool) (*big.Int, error) {
	out, err := SwapV3Into(NewScratch(), in, p, zeroForOne)
	if err != nil {
		return nil, err
	}
	return new(big.Int).Set(out), nil
}

// SwapV3Into returns the output amount for an exact-input swap, using sc for
// all intermediate values.
//
// zeroForOne selects the direction: true sells token0 for token1 and pushes
// the price down, false does the reverse.
//
// The algorithm walks the price curve in steps. Each step swaps within the
// current tick range, and if the input is large enough to reach the range
// boundary it crosses the tick, adjusts active liquidity, and continues. This
// is what makes a V3 quote correct for large trades: liquidity is not uniform
// across price, so a single-range approximation overstates the output of any
// trade big enough to matter.
//
// The pool state is never mutated, so one snapshot can be quoted concurrently.
// The RETURNED VALUE is owned by sc and is overwritten by the next call that
// uses the same Scratch. Copy it if you need to keep it.
func SwapV3Into(sc *Scratch, in *big.Int, p V3Pool, zeroForOne bool) (*big.Int, error) {
	if !isPositive(in) {
		return nil, ErrZeroAmount
	}
	if p.FeeBps >= bpsDenominator {
		return nil, ErrInvalidFee
	}
	if !isPositive(p.Liquidity) || !isPositive(p.SqrtPriceX96) {
		return nil, ErrInsufficientLiquidity
	}

	ticks := sortedTicks(p.Ticks)

	sc.remaining.Set(in)
	sc.out.SetInt64(0)
	sc.sqrtPrice.Set(p.SqrtPriceX96)
	sc.liquidity.Set(p.Liquidity)

	currentTick := p.Tick
	fee := p.feePips()

	// Each iteration either exhausts the input or crosses exactly one tick, so
	// the loop is bounded by the size of the tick window.
	for i := 0; i <= len(ticks); i++ {
		if sc.remaining.Sign() <= 0 {
			return sc.out, nil
		}
		if sc.liquidity.Sign() <= 0 {
			return nil, ErrInsufficientLiquidity
		}

		next, ok := nextInitializedTick(ticks, currentTick, zeroForOne)
		if !ok {
			// The window ended before the input was consumed.
			return nil, ErrInsufficientLiquidity
		}

		targetSqrt := next.SqrtRatioX96
		if targetSqrt == nil {
			var err error
			if targetSqrt, err = GetSqrtRatioAtTick(next.Index); err != nil {
				return nil, err
			}
		}

		reachedTarget := sc.computeSwapStep(targetSqrt, fee, zeroForOne)

		sc.out.Add(sc.out, sc.stepOut)
		sc.remaining.Sub(sc.remaining, sc.stepIn)
		sc.sqrtPrice.Set(sc.stepNext)

		if sc.remaining.Sign() <= 0 {
			return sc.out, nil
		}

		// The step consumed less than the remaining input, so it must have
		// stopped at the tick boundary. Anything else means no further
		// progress is possible, and continuing would spin.
		if !reachedTarget {
			return nil, ErrInsufficientLiquidity
		}

		if zeroForOne {
			sc.liquidity.Sub(sc.liquidity, next.LiquidityNet)
			currentTick = next.Index - 1
		} else {
			sc.liquidity.Add(sc.liquidity, next.LiquidityNet)
			currentTick = next.Index
		}
	}

	if sc.remaining.Sign() > 0 {
		return nil, ErrInsufficientLiquidity
	}
	return sc.out, nil
}

// computeSwapStep swaps within a single tick range, stopping either when the
// input is exhausted or when the price reaches the range boundary.
//
// It reads sc.sqrtPrice, sc.liquidity and sc.remaining, and writes sc.stepNext
// (the price after the step), sc.stepIn (input consumed including fee) and
// sc.stepOut (output produced). It reports whether the boundary was reached.
func (sc *Scratch) computeSwapStep(sqrtTarget *big.Int, feePips int64, zeroForOne bool) bool {
	// Take the fee off the input before it moves the price. What is left is
	// what actually trades.
	sc.lessFee.Mul(sc.remaining, big.NewInt(pipDenominator-feePips))
	sc.lessFee.Div(sc.lessFee, big.NewInt(pipDenominator))

	// How much input would be needed to walk the price all the way to the
	// boundary of this range.
	if zeroForOne {
		sc.getAmount0DeltaInto(sc.inToTarget, sqrtTarget, sc.sqrtPrice, true)
	} else {
		sc.getAmount1DeltaInto(sc.inToTarget, sc.sqrtPrice, sqrtTarget, true)
	}

	reachedTarget := sc.lessFee.Cmp(sc.inToTarget) >= 0

	if reachedTarget {
		sc.stepNext.Set(sqrtTarget)
	} else {
		sc.getNextSqrtPriceFromInputInto(sc.stepNext, sc.lessFee, zeroForOne)
	}

	if zeroForOne {
		if reachedTarget {
			sc.stepIn.Set(sc.inToTarget)
		} else {
			sc.getAmount0DeltaInto(sc.stepIn, sc.stepNext, sc.sqrtPrice, true)
		}
		sc.getAmount1DeltaInto(sc.stepOut, sc.stepNext, sc.sqrtPrice, false)
	} else {
		if reachedTarget {
			sc.stepIn.Set(sc.inToTarget)
		} else {
			sc.getAmount1DeltaInto(sc.stepIn, sc.sqrtPrice, sc.stepNext, true)
		}
		sc.getAmount0DeltaInto(sc.stepOut, sc.sqrtPrice, sc.stepNext, false)
	}

	// Add the fee back on, so the caller deducts the full cost of the step.
	if !reachedTarget {
		// The step ended because the input ran out, so the fee is whatever was
		// held back. Charging the computed amount instead would leave a dust
		// remainder and spin the outer loop.
		sc.feeAmount.Sub(sc.remaining, sc.stepIn)
		if sc.feeAmount.Sign() < 0 {
			sc.feeAmount.SetInt64(0)
		}
	} else {
		sc.mulDivRoundingUpInto(sc.feeAmount, sc.stepIn, big.NewInt(feePips), big.NewInt(pipDenominator-feePips))
	}

	sc.stepIn.Add(sc.stepIn, sc.feeAmount)
	return reachedTarget
}

// getAmount0DeltaInto sets dst to the token0 amount between two prices for the
// current liquidity: L * (sqrtB - sqrtA) * 2^96 / (sqrtA * sqrtB).
func (sc *Scratch) getAmount0DeltaInto(dst, sqrtA, sqrtB *big.Int, roundUp bool) {
	if sqrtA.Cmp(sqrtB) > 0 {
		sqrtA, sqrtB = sqrtB, sqrtA
	}
	if sqrtA.Sign() <= 0 {
		dst.SetInt64(0)
		return
	}

	sc.a0t0.Lsh(sc.liquidity, 96)
	sc.a0t1.Sub(sqrtB, sqrtA)

	if roundUp {
		sc.mulDivRoundingUpInto(dst, sc.a0t0, sc.a0t1, sqrtB)
		sc.divRoundingUpInto(dst, sqrtA)
		return
	}
	sc.mulDivInto(dst, sc.a0t0, sc.a0t1, sqrtB)
	dst.Div(dst, sqrtA)
}

// getAmount1DeltaInto sets dst to the token1 amount between two prices for the
// current liquidity: L * (sqrtB - sqrtA) / 2^96.
func (sc *Scratch) getAmount1DeltaInto(dst, sqrtA, sqrtB *big.Int, roundUp bool) {
	if sqrtA.Cmp(sqrtB) > 0 {
		sqrtA, sqrtB = sqrtB, sqrtA
	}
	sc.a1t0.Sub(sqrtB, sqrtA)

	if roundUp {
		sc.mulDivRoundingUpInto(dst, sc.liquidity, sc.a1t0, q96)
		return
	}
	sc.mulDivInto(dst, sc.liquidity, sc.a1t0, q96)
}

// getNextSqrtPriceFromInputInto sets dst to the price after adding amount of
// the input token to the pool at the current price and liquidity.
func (sc *Scratch) getNextSqrtPriceFromInputInto(dst, amount *big.Int, zeroForOne bool) {
	if amount.Sign() == 0 {
		dst.Set(sc.sqrtPrice)
		return
	}

	if zeroForOne {
		// Adding token0 lowers the price:
		//   sqrtP' = L * sqrtP / (L + amount * sqrtP / 2^96)
		sc.ns0.Lsh(sc.liquidity, 96)
		sc.ns1.Mul(amount, sc.sqrtPrice)
		sc.ns1.Add(sc.ns0, sc.ns1)
		sc.mulDivRoundingUpInto(dst, sc.ns0, sc.sqrtPrice, sc.ns1)
		return
	}

	// Adding token1 raises the price: sqrtP' = sqrtP + amount * 2^96 / L
	sc.ns0.Lsh(amount, 96)
	sc.ns0.Div(sc.ns0, sc.liquidity)
	dst.Add(sc.sqrtPrice, sc.ns0)
}

// sortedTicks returns the ticks in ascending index order.
//
// Ticks that are already ordered (the normal case, because the snapshot
// builder runs PrepareTicks) are returned as-is. Only an unsorted slice pays
// for a defensive copy, which keeps the shared snapshot safe for concurrent
// readers without charging every quote for the guarantee.
func sortedTicks(ticks []V3Tick) []V3Tick {
	if isSorted(ticks) {
		return ticks
	}

	out := make([]V3Tick, len(ticks))
	copy(out, ticks)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func isSorted(ticks []V3Tick) bool {
	for i := 1; i < len(ticks); i++ {
		if ticks[i].Index < ticks[i-1].Index {
			return false
		}
	}
	return true
}

// nextInitializedTick returns the next tick boundary the price will reach.
//
// Selling token0 pushes the price down, so the next boundary is the highest
// initialised tick at or below the current one. Selling token1 pushes it up,
// so the next boundary is the lowest initialised tick strictly above.
func nextInitializedTick(ticks []V3Tick, current int32, zeroForOne bool) (V3Tick, bool) {
	if zeroForOne {
		for i := len(ticks) - 1; i >= 0; i-- {
			if ticks[i].Index <= current {
				return ticks[i], true
			}
		}
		return V3Tick{}, false
	}

	for i := range ticks {
		if ticks[i].Index > current {
			return ticks[i], true
		}
	}
	return V3Tick{}, false
}
