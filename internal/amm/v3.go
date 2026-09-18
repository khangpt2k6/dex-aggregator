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
func (p V3Pool) feePips() *big.Int {
	return big.NewInt(int64(p.FeeBps) * 100)
}

const pipDenominator = 1_000_000

// SwapV3 returns the output amount for an exact-input swap against a
// concentrated liquidity pool.
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
// Inputs are never mutated.
func SwapV3(in *big.Int, p V3Pool, zeroForOne bool) (*big.Int, error) {
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

	remaining := new(big.Int).Set(in)
	out := new(big.Int)

	sqrtPrice := new(big.Int).Set(p.SqrtPriceX96)
	liquidity := new(big.Int).Set(p.Liquidity)
	currentTick := p.Tick

	fee := p.feePips()

	// Each iteration either exhausts the input or crosses exactly one tick, so
	// the loop is bounded by the size of the tick window.
	for i := 0; i <= len(ticks); i++ {
		if remaining.Sign() <= 0 {
			return out, nil
		}
		if liquidity.Sign() <= 0 {
			return nil, ErrInsufficientLiquidity
		}

		next, ok := nextInitializedTick(ticks, currentTick, zeroForOne)
		if !ok {
			// The window ended before the input was consumed.
			return nil, ErrInsufficientLiquidity
		}

		targetSqrt, err := GetSqrtRatioAtTick(next.Index)
		if err != nil {
			return nil, err
		}

		nextSqrt, amountIn, amountOut := computeSwapStep(sqrtPrice, targetSqrt, liquidity, remaining, fee, zeroForOne)

		out.Add(out, amountOut)

		// amountIn already includes the fee taken on this step.
		remaining.Sub(remaining, amountIn)
		sqrtPrice = nextSqrt

		if remaining.Sign() <= 0 {
			return out, nil
		}

		// The step consumed less than the remaining input, so it must have
		// stopped at the tick boundary. Cross it.
		if nextSqrt.Cmp(targetSqrt) != 0 {
			// Neither exhausted nor at the boundary means no further progress
			// is possible; stopping here avoids an infinite loop.
			return nil, ErrInsufficientLiquidity
		}

		if zeroForOne {
			liquidity.Sub(liquidity, next.LiquidityNet)
			currentTick = next.Index - 1
		} else {
			liquidity.Add(liquidity, next.LiquidityNet)
			currentTick = next.Index
		}
	}

	if remaining.Sign() > 0 {
		return nil, ErrInsufficientLiquidity
	}
	return out, nil
}

// computeSwapStep swaps within a single tick range, stopping either when the
// input is exhausted or when the price reaches the range boundary.
//
// It returns the price after the step, the input consumed including fee, and
// the output produced.
func computeSwapStep(sqrtCurrent, sqrtTarget, liquidity, remaining, feePips *big.Int, zeroForOne bool) (nextSqrt, amountIn, amountOut *big.Int) {
	// Take the fee off the input before it moves the price. What is left is
	// what actually trades.
	remainingLessFee := mulDiv(remaining, big.NewInt(pipDenominator-feePips.Int64()), big.NewInt(pipDenominator))

	// How much input would be needed to walk the price all the way to the
	// boundary of this range.
	var amountInToTarget *big.Int
	if zeroForOne {
		amountInToTarget = getAmount0Delta(sqrtTarget, sqrtCurrent, liquidity, true)
	} else {
		amountInToTarget = getAmount1Delta(sqrtCurrent, sqrtTarget, liquidity, true)
	}

	reachedTarget := remainingLessFee.Cmp(amountInToTarget) >= 0

	if reachedTarget {
		nextSqrt = new(big.Int).Set(sqrtTarget)
	} else {
		nextSqrt = getNextSqrtPriceFromInput(sqrtCurrent, liquidity, remainingLessFee, zeroForOne)
	}

	if zeroForOne {
		if reachedTarget {
			amountIn = amountInToTarget
		} else {
			amountIn = getAmount0Delta(nextSqrt, sqrtCurrent, liquidity, true)
		}
		amountOut = getAmount1Delta(nextSqrt, sqrtCurrent, liquidity, false)
	} else {
		if reachedTarget {
			amountIn = amountInToTarget
		} else {
			amountIn = getAmount1Delta(sqrtCurrent, nextSqrt, liquidity, true)
		}
		amountOut = getAmount0Delta(sqrtCurrent, nextSqrt, liquidity, false)
	}

	// Add the fee back on, so the caller deducts the full cost of the step.
	var feeAmount *big.Int
	if !reachedTarget {
		// The step ended because the input ran out, so the fee is whatever was
		// held back. Charging the computed amount instead would leave a dust
		// remainder and spin the outer loop.
		feeAmount = new(big.Int).Sub(remaining, amountIn)
		if feeAmount.Sign() < 0 {
			feeAmount = new(big.Int)
		}
	} else {
		feeAmount = mulDivRoundingUp(amountIn, feePips, big.NewInt(pipDenominator-feePips.Int64()))
	}

	amountIn = new(big.Int).Add(amountIn, feeAmount)
	return nextSqrt, amountIn, amountOut
}

// getAmount0Delta returns the token0 amount between two prices for a given
// liquidity: L * (sqrtB - sqrtA) * 2^96 / (sqrtA * sqrtB).
func getAmount0Delta(sqrtA, sqrtB, liquidity *big.Int, roundUp bool) *big.Int {
	if sqrtA.Cmp(sqrtB) > 0 {
		sqrtA, sqrtB = sqrtB, sqrtA
	}
	if sqrtA.Sign() <= 0 {
		return new(big.Int)
	}

	numerator1 := new(big.Int).Lsh(liquidity, 96)
	numerator2 := new(big.Int).Sub(sqrtB, sqrtA)

	if roundUp {
		return divRoundingUp(mulDivRoundingUp(numerator1, numerator2, sqrtB), sqrtA)
	}
	return new(big.Int).Div(mulDiv(numerator1, numerator2, sqrtB), sqrtA)
}

// getAmount1Delta returns the token1 amount between two prices for a given
// liquidity: L * (sqrtB - sqrtA) / 2^96.
func getAmount1Delta(sqrtA, sqrtB, liquidity *big.Int, roundUp bool) *big.Int {
	if sqrtA.Cmp(sqrtB) > 0 {
		sqrtA, sqrtB = sqrtB, sqrtA
	}
	delta := new(big.Int).Sub(sqrtB, sqrtA)

	if roundUp {
		return mulDivRoundingUp(liquidity, delta, q96)
	}
	return mulDiv(liquidity, delta, q96)
}

// getNextSqrtPriceFromInput returns the price after adding amount of the input
// token to the pool.
func getNextSqrtPriceFromInput(sqrtPrice, liquidity, amount *big.Int, zeroForOne bool) *big.Int {
	if amount.Sign() == 0 {
		return new(big.Int).Set(sqrtPrice)
	}

	if zeroForOne {
		// Adding token0 lowers the price:
		//   sqrtP' = L * sqrtP / (L + amount * sqrtP / 2^96)
		numerator1 := new(big.Int).Lsh(liquidity, 96)
		product := new(big.Int).Mul(amount, sqrtPrice)
		denominator := new(big.Int).Add(numerator1, product)
		return mulDivRoundingUp(numerator1, sqrtPrice, denominator)
	}

	// Adding token1 raises the price: sqrtP' = sqrtP + amount * 2^96 / L
	quotient := new(big.Int).Lsh(amount, 96)
	quotient.Div(quotient, liquidity)
	return new(big.Int).Add(sqrtPrice, quotient)
}

// sortedTicks returns the ticks in ascending index order without mutating the
// caller's slice, so a shared snapshot stays safe for concurrent readers.
func sortedTicks(ticks []V3Tick) []V3Tick {
	out := make([]V3Tick, len(ticks))
	copy(out, ticks)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
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
