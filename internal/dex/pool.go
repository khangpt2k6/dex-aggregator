// Package dex models liquidity pools and the sources that supply them.
//
// A Pool holds protocol-neutral state. Direction is resolved at call time by
// AmountOut, so the rest of the system can treat a Uniswap V3 pool and a
// Sushiswap V2 pool as the same kind of thing: something that turns an input
// amount of one token into an output amount of another.
package dex

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
)

// Protocol identifies which AMM implementation a pool follows.
type Protocol string

const (
	UniswapV3   Protocol = "uniswap-v3"
	SushiswapV2 Protocol = "sushiswap-v2"
)

var (
	// ErrTokenNotInPool is returned when the requested input token is not one
	// of the pool's two tokens.
	ErrTokenNotInPool = errors.New("dex: token is not in this pool")

	// ErrPoolStateMissing is returned when a pool has been discovered but its
	// on-chain state has not been loaded yet.
	ErrPoolStateMissing = errors.New("dex: pool state not loaded")

	// ErrUnknownProtocol is returned for a pool with no supported protocol.
	ErrUnknownProtocol = errors.New("dex: unknown protocol")
)

// Token is an ERC-20 the aggregator knows how to route through.
type Token struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Decimals uint8  `json:"decimals"`
}

// Pool is one liquidity venue between two tokens.
//
// The state fields are protocol-specific: a V2 pool populates Reserve0 and
// Reserve1, a V3 pool populates SqrtPriceX96, Liquidity, Tick and Ticks. The
// unused set stays nil rather than being carried in a separate struct, because
// a pool is read far more often than it is built and the flat layout keeps the
// hot path free of pointer chasing.
type Pool struct {
	Address  string   `json:"address"`
	Protocol Protocol `json:"protocol"`
	Token0   Token    `json:"token0"`
	Token1   Token    `json:"token1"`
	FeeBps   uint32   `json:"feeBps"`

	// Simulated marks state that came from the deterministic simulator rather
	// than from a node. It is surfaced all the way to the UI so that demo data
	// is never mistaken for live data.
	Simulated bool `json:"simulated"`

	// V2 state.
	Reserve0 *big.Int `json:"-"`
	Reserve1 *big.Int `json:"-"`

	// V3 state.
	SqrtPriceX96 *big.Int     `json:"-"`
	Liquidity    *big.Int     `json:"-"`
	Tick         int32        `json:"tick,omitempty"`
	TickSpacing  int32        `json:"tickSpacing,omitempty"`
	Ticks        []amm.V3Tick `json:"-"`
}

// Has reports whether the given address is one of the pool's tokens.
func (p *Pool) Has(address string) bool {
	return addrEqual(address, p.Token0.Address) || addrEqual(address, p.Token1.Address)
}

// OtherToken returns the token on the far side of the pool from address.
func (p *Pool) OtherToken(address string) (Token, bool) {
	switch {
	case addrEqual(address, p.Token0.Address):
		return p.Token1, true
	case addrEqual(address, p.Token1.Address):
		return p.Token0, true
	default:
		return Token{}, false
	}
}

// AmountOut quotes selling amountIn of tokenIn into this pool.
//
// The quote accounts for price impact at the requested size, so the answer for
// a large trade is genuinely different from scaling up the answer for a small
// one. That is the whole point: the router compares pools at the size actually
// being traded.
//
// Pool state is never mutated, so a single snapshot is safe to quote against
// from many goroutines at once.
func (p *Pool) AmountOut(amountIn *big.Int, tokenIn string) (*big.Int, error) {
	out, err := p.AmountOutInto(amm.NewScratch(), amountIn, tokenIn)
	if err != nil {
		return nil, err
	}
	return new(big.Int).Set(out), nil
}

// AmountOutInto is AmountOut using sc for intermediate values.
//
// The RETURNED VALUE is owned by sc and is overwritten by the next call using
// the same Scratch. Callers that keep the result must copy it. The router does
// exactly that, and only for the amounts that actually win, which is what
// keeps a routing query from generating hundreds of kilobytes of garbage.
func (p *Pool) AmountOutInto(sc *amm.Scratch, amountIn *big.Int, tokenIn string) (*big.Int, error) {
	zeroForOne, err := p.direction(tokenIn)
	if err != nil {
		return nil, err
	}

	switch p.Protocol {
	case SushiswapV2:
		return p.amountOutV2(sc, amountIn, zeroForOne)
	case UniswapV3:
		return p.amountOutV3(sc, amountIn, zeroForOne)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProtocol, p.Protocol)
	}
}

// direction reports whether the swap sells token0 for token1.
func (p *Pool) direction(tokenIn string) (bool, error) {
	switch {
	case addrEqual(tokenIn, p.Token0.Address):
		return true, nil
	case addrEqual(tokenIn, p.Token1.Address):
		return false, nil
	default:
		return false, fmt.Errorf("%w: %s not in %s/%s", ErrTokenNotInPool, tokenIn, p.Token0.Symbol, p.Token1.Symbol)
	}
}

func (p *Pool) amountOutV2(sc *amm.Scratch, amountIn *big.Int, zeroForOne bool) (*big.Int, error) {
	if p.Reserve0 == nil || p.Reserve1 == nil {
		return nil, fmt.Errorf("%w: pool %s has no reserves", ErrPoolStateMissing, p.Address)
	}

	oriented := amm.V2Pool{FeeBps: p.FeeBps}
	if zeroForOne {
		oriented.ReserveIn, oriented.ReserveOut = p.Reserve0, p.Reserve1
	} else {
		oriented.ReserveIn, oriented.ReserveOut = p.Reserve1, p.Reserve0
	}
	return amm.SwapV2Into(sc, amountIn, oriented)
}

func (p *Pool) amountOutV3(sc *amm.Scratch, amountIn *big.Int, zeroForOne bool) (*big.Int, error) {
	if p.SqrtPriceX96 == nil || p.Liquidity == nil {
		return nil, fmt.Errorf("%w: pool %s has no slot0", ErrPoolStateMissing, p.Address)
	}

	return amm.SwapV3Into(sc, amountIn, amm.V3Pool{
		SqrtPriceX96: p.SqrtPriceX96,
		Liquidity:    p.Liquidity,
		Tick:         p.Tick,
		FeeBps:       p.FeeBps,
		TickSpacing:  p.TickSpacing,
		Ticks:        p.Ticks,
	}, zeroForOne)
}

// PoolSource supplies pool state from somewhere: a node, a simulator, a cache.
//
// Implementations are swapped at wiring time and nowhere else, which is what
// lets the service run identically with and without an RPC endpoint.
type PoolSource interface {
	// Name identifies the source in logs and in the pools endpoint.
	Name() string

	// Pools returns every pool this source currently knows about, with state
	// loaded. It is called on every refresh, so it should be cheap enough to
	// run on the refresh interval.
	Pools(ctx context.Context) ([]Pool, error)
}

// addrEqual compares Ethereum addresses ignoring case.
//
// Addresses reach us from three places with three conventions: checksummed
// from a token list, lowercase from RPC, and whatever the user typed. Treating
// them as case sensitive would produce routes that mysteriously fail to find
// pools that are right there.
func addrEqual(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}
