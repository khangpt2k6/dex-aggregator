package amm

import "math/big"

// V2Pool is one direction of a constant product pool, already oriented so that
// ReserveIn is the reserve of the token being sold and ReserveOut the reserve
// of the token being bought. Orienting at construction keeps the swap function
// free of direction branching.
type V2Pool struct {
	ReserveIn  *big.Int
	ReserveOut *big.Int

	// FeeBps is the swap fee in basis points. Uniswap V2 and Sushiswap both
	// charge 30 bps.
	FeeBps uint32
}

// SwapV2 returns the output amount for selling in into a constant product pool.
//
// The invariant is x*y = k. Taking the fee off the input first:
//
//	inAfterFee = in * (10000 - feeBps)
//	out        = (inAfterFee * reserveOut) / (reserveIn * 10000 + inAfterFee)
//
// The factor of 10000 is carried through rather than dividing early, so the
// only truncation happens once, at the final division, and always downward.
// Rounding down is the correct direction: quoting more than the pool can pay
// would make every quote a lie.
//
// Output is strictly less than ReserveOut for any finite input, so the pool
// cannot be drained. Output is also concave in input, which is precisely why a
// router must know the trade size before it can compare two pools.
//
// Inputs are never mutated.
func SwapV2(in *big.Int, p V2Pool) (*big.Int, error) {
	out, err := SwapV2Into(NewScratch(), in, p)
	if err != nil {
		return nil, err
	}
	return new(big.Int).Set(out), nil
}

// SwapV2Into is SwapV2 using sc for intermediate values.
//
// The RETURNED VALUE is owned by sc and is overwritten by the next call using
// the same Scratch. Copy it if you need to keep it.
func SwapV2Into(sc *Scratch, in *big.Int, p V2Pool) (*big.Int, error) {
	if !isPositive(in) {
		return nil, ErrZeroAmount
	}
	if p.FeeBps >= bpsDenominator {
		return nil, ErrInvalidFee
	}
	if !isPositive(p.ReserveIn) || !isPositive(p.ReserveOut) {
		return nil, ErrInsufficientLiquidity
	}

	// inAfterFee = in * (10000 - feeBps)
	sc.v2a.Mul(in, big.NewInt(int64(bpsDenominator-p.FeeBps)))

	// denominator = reserveIn * 10000 + inAfterFee
	sc.v2b.Mul(p.ReserveIn, big.NewInt(bpsDenominator))
	sc.v2b.Add(sc.v2b, sc.v2a)

	// out = inAfterFee * reserveOut / denominator
	sc.out.Mul(sc.v2a, p.ReserveOut)
	sc.out.Div(sc.out, sc.v2b)

	return sc.out, nil
}

// SpotPriceV2 returns the marginal exchange rate at zero trade size, scaled by
// 1e18 to keep it an integer.
//
// This is the number a naive router would use as an edge weight. It is exposed
// only so that price impact can be reported as the gap between the marginal
// rate and the realised rate. It must not be used to choose a route.
func SpotPriceV2(p V2Pool) (*big.Int, error) {
	if !isPositive(p.ReserveIn) || !isPositive(p.ReserveOut) {
		return nil, ErrInsufficientLiquidity
	}
	scaled := new(big.Int).Mul(p.ReserveOut, scale1e18)
	return scaled.Div(scaled, p.ReserveIn), nil
}

// scale1e18 is the fixed-point scale used for reported prices.
var scale1e18 = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
