package amm

import "math/big"

// Scratch is reusable big.Int workspace for the swap math.
//
// Every big.Int operation allocates a fresh backing array unless the receiver
// already has the capacity to hold the result. A single routing query evaluates
// well over a thousand candidate swaps, each running a multi-step tick walk, so
// the naive version generates hundreds of kilobytes of garbage per request. The
// resulting garbage collection is not a cost the average request pays evenly:
// it lands as multi-millisecond pauses on whichever unlucky request happens to
// be running, which is exactly the p99 the service is trying to hold down.
//
// Reusing one Scratch across a whole query lets those receivers keep their
// backing arrays, turning the steady-state allocation of a quote into roughly
// nothing.
//
// A Scratch is NOT safe for concurrent use. Each goroutine handling a request
// needs its own. The values it returns are only valid until the next call that
// uses the same Scratch, so a caller that needs to keep a result must copy it.
type Scratch struct {
	// Swap loop state.
	remaining  *big.Int
	out        *big.Int
	sqrtPrice  *big.Int
	liquidity  *big.Int
	stepNext   *big.Int
	stepIn     *big.Int
	stepOut    *big.Int
	lessFee    *big.Int
	inToTarget *big.Int
	feeAmount  *big.Int

	// Private temporaries, named per helper so nested calls cannot collide.
	a0t0, a0t1 *big.Int
	a1t0       *big.Int
	ns0, ns1   *big.Int
	rem        *big.Int
	one        *big.Int

	// V2 temporaries.
	v2a, v2b *big.Int
}

// NewScratch allocates a workspace. Allocate one per goroutine and reuse it.
func NewScratch() *Scratch {
	return &Scratch{
		remaining:  new(big.Int),
		out:        new(big.Int),
		sqrtPrice:  new(big.Int),
		liquidity:  new(big.Int),
		stepNext:   new(big.Int),
		stepIn:     new(big.Int),
		stepOut:    new(big.Int),
		lessFee:    new(big.Int),
		inToTarget: new(big.Int),
		feeAmount:  new(big.Int),
		a0t0:       new(big.Int),
		a0t1:       new(big.Int),
		a1t0:       new(big.Int),
		ns0:        new(big.Int),
		ns1:        new(big.Int),
		rem:        new(big.Int),
		one:        big.NewInt(1),
		v2a:        new(big.Int),
		v2b:        new(big.Int),
	}
}

// mulDivInto sets dst to a*b/denominator, truncating. dst must not alias the
// operands.
func (s *Scratch) mulDivInto(dst, a, b, denominator *big.Int) {
	dst.Mul(a, b)
	dst.Div(dst, denominator)
}

// mulDivRoundingUpInto sets dst to ceil(a*b/denominator).
func (s *Scratch) mulDivRoundingUpInto(dst, a, b, denominator *big.Int) {
	dst.Mul(a, b)
	dst.QuoRem(dst, denominator, s.rem)
	if s.rem.Sign() != 0 {
		dst.Add(dst, s.one)
	}
}

// divRoundingUpInto sets dst to ceil(dst/denominator).
func (s *Scratch) divRoundingUpInto(dst, denominator *big.Int) {
	dst.QuoRem(dst, denominator, s.rem)
	if s.rem.Sign() != 0 {
		dst.Add(dst, s.one)
	}
}
