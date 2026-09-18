package amm

import "math/big"

// Uniswap V3 divides price space into ticks, where tick i corresponds to
// price 1.0001^i. These are the protocol bounds.
const (
	MinTick int32 = -887272
	MaxTick int32 = 887272
)

var (
	q96  = new(big.Int).Lsh(big.NewInt(1), 96)
	q128 = new(big.Int).Lsh(big.NewInt(1), 128)

	// uint256Max is the value the reciprocal step divides into, matching the
	// on-chain implementation exactly.
	uint256Max = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))

	minSqrtRatio = big.NewInt(4295128739)
	maxSqrtRatio = mustParse("1461446703485210103287273052203988822378723970342")

	// sqrtRatioFactors is the fixed-point bit decomposition of 1.0001^(-1/2)
	// raised to successive powers of two, in Q128. Multiplying the factors
	// selected by the set bits of |tick| reconstructs the ratio in 20 steps
	// instead of an exponentiation, which is what makes this cheap enough to
	// call inside a routing loop.
	//
	// Index k holds the factor for bit 1<<k.
	sqrtRatioFactors = [20]*big.Int{
		mustParseHex("fffcb933bd6fad37aa2d162d1a594001"),
		mustParseHex("fff97272373d413259a46990580e213a"),
		mustParseHex("fff2e50f5f656932ef12357cf3c7fdcc"),
		mustParseHex("ffe5caca7e10e4e61c3624eaa0941cd0"),
		mustParseHex("ffcb9843d60f6159c9db58835c926644"),
		mustParseHex("ff973b41fa98c081472e6896dfb254c0"),
		mustParseHex("ff2ea16466c96a3843ec78b326b52861"),
		mustParseHex("fe5dee046a99a2a811c461f1969c3053"),
		mustParseHex("fcbe86c7900a88aedcffc83b479aa3a4"),
		mustParseHex("f987a7253ac413176f2b074cf7815e54"),
		mustParseHex("f3392b0822b70005940c7a398e4b70f3"),
		mustParseHex("e7159475a2c29b7443b29c7fa6e889d9"),
		mustParseHex("d097f3bdfd2022b8845ad8f792aa5825"),
		mustParseHex("a9f746462d870fdf8a65dc1f90e061e5"),
		mustParseHex("70d869a156d2a1b890bb3df62baf32f7"),
		mustParseHex("31be135f97d08fd981231505542fcfa6"),
		mustParseHex("9aa508b5b7a84e1c677de54f3e99bc9"),
		mustParseHex("5d6af8dedb81196699c329225ee604"),
		mustParseHex("2216e584f5fa1ea926041bedfe98"),
		mustParseHex("48a170391f7dc42444e8fa2"),
	}
)

// MinSqrtRatio returns a copy of the lowest representable sqrt price.
func MinSqrtRatio() *big.Int { return new(big.Int).Set(minSqrtRatio) }

// MaxSqrtRatio returns a copy of the highest representable sqrt price.
func MaxSqrtRatio() *big.Int { return new(big.Int).Set(maxSqrtRatio) }

// GetSqrtRatioAtTick returns sqrt(1.0001^tick) in Q64.96 fixed point.
//
// This is a direct port of the on-chain TickMath library rather than a
// floating point approximation, because the result feeds amount arithmetic and
// must agree with what the pool contract itself would compute.
func GetSqrtRatioAtTick(tick int32) (*big.Int, error) {
	if tick < MinTick || tick > MaxTick {
		return nil, ErrTickOutOfRange
	}

	absTick := tick
	if absTick < 0 {
		absTick = -absTick
	}

	// Start from 1.0 in Q128, or from the bit-0 factor if that bit is set.
	ratio := new(big.Int)
	if absTick&0x1 != 0 {
		ratio.Set(sqrtRatioFactors[0])
	} else {
		ratio.Set(q128)
	}

	for k := 1; k < len(sqrtRatioFactors); k++ {
		if absTick&(1<<uint(k)) != 0 {
			ratio.Mul(ratio, sqrtRatioFactors[k])
			ratio.Rsh(ratio, 128)
		}
	}

	// The table encodes negative ticks. A positive tick is the reciprocal.
	if tick > 0 {
		ratio.Div(uint256Max, ratio)
	}

	// Convert Q128 to Q96, rounding up so the result never understates the
	// price, matching the on-chain rounding direction.
	remainder := new(big.Int).And(ratio, big.NewInt(0xFFFFFFFF))
	result := new(big.Int).Rsh(ratio, 32)
	if remainder.Sign() != 0 {
		result.Add(result, big.NewInt(1))
	}
	return result, nil
}

// mulDiv computes a*b/denominator, truncating toward zero.
func mulDiv(a, b, denominator *big.Int) *big.Int {
	out := new(big.Int).Mul(a, b)
	return out.Div(out, denominator)
}

// mulDivRoundingUp computes ceil(a*b/denominator).
func mulDivRoundingUp(a, b, denominator *big.Int) *big.Int {
	out := new(big.Int).Mul(a, b)
	quotient, remainder := new(big.Int).QuoRem(out, denominator, new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

// divRoundingUp computes ceil(a/denominator).
func divRoundingUp(a, denominator *big.Int) *big.Int {
	quotient, remainder := new(big.Int).QuoRem(a, denominator, new(big.Int))
	if remainder.Sign() != 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	return quotient
}

func mustParse(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("amm: bad decimal constant " + s)
	}
	return n
}

func mustParseHex(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 16)
	if !ok {
		panic("amm: bad hex constant " + s)
	}
	return n
}
