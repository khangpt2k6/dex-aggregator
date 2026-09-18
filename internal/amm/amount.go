// Package amm implements automated market maker swap math.
//
// Everything that determines an amount is computed in big.Int. Floating point
// appears nowhere in this package, because a rounding error here is a rounding
// error in someone's money. Callers that need a float (the router, when it
// converts an exchange ratio into a logarithmic edge weight) do the conversion
// themselves, at the point where the loss of precision is harmless.
//
// The package knows nothing about RPC, Redis, or HTTP. It takes pool state and
// an input amount and returns an output amount.
package amm

import (
	"errors"
	"math/big"
)

var (
	// ErrZeroAmount is returned for a non-positive input amount.
	ErrZeroAmount = errors.New("amm: input amount must be positive")

	// ErrInsufficientLiquidity is returned when a pool cannot serve the swap,
	// either because a reserve is empty or because the tick window ran out
	// before the input was consumed.
	ErrInsufficientLiquidity = errors.New("amm: insufficient liquidity")

	// ErrInvalidFee is returned for a fee outside the representable range.
	ErrInvalidFee = errors.New("amm: fee must be below 100 percent")

	// ErrTickOutOfRange is returned for a tick beyond the protocol bounds.
	ErrTickOutOfRange = errors.New("amm: tick out of range")
)

// bpsDenominator is the basis-point scale: 10000 bps is 100 percent.
const bpsDenominator = 10000

// isPositive reports whether n is non-nil and strictly greater than zero.
func isPositive(n *big.Int) bool {
	return n != nil && n.Sign() > 0
}
