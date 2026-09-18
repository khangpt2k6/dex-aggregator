package onchain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/rpc"
)

// tickWindow is how many initialised tick positions to read on each side of
// the current price.
//
// Reading a pool's whole tick map would cost hundreds of calls per pool and
// return liquidity at prices no realistic trade reaches. This window covers the
// range ordinary trades move through; a swap that runs past it is reported as
// unquotable rather than extrapolated, which is the honest answer.
const tickWindow = 8

// tickStride is how many tick spacings apart to sample the window.
const tickStride = 20

// UniswapV3 reads concentrated liquidity pools.
type UniswapV3 struct {
	mc       *rpc.Multicaller
	tokens   []dex.Token
	feeTiers []uint32
}

// NewUniswapV3 returns a source covering every pair of the given tokens at
// every given fee tier.
func NewUniswapV3(mc *rpc.Multicaller, tokens []dex.Token, feeTiers []uint32) *UniswapV3 {
	if len(feeTiers) == 0 {
		feeTiers = DefaultV3FeeTiers
	}
	return &UniswapV3{mc: mc, tokens: tokens, feeTiers: feeTiers}
}

// Name identifies the source in logs and in the pools endpoint.
func (u *UniswapV3) Name() string { return "uniswap-v3" }

type v3Candidate struct {
	address string
	token0  dex.Token
	token1  dex.Token
	feeBps  uint32
}

// Pools reads slot0, liquidity and a tick window for every derived pool.
//
// This runs as two batched passes. The first asks every candidate address for
// slot0 and liquidity, which is how it learns both which pools exist and where
// their prices currently sit. Only then can the second pass know which ticks
// are worth reading, since the window is relative to the current tick.
func (u *UniswapV3) Pools(ctx context.Context) ([]dex.Pool, error) {
	candidates, err := u.candidates()
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	slot0Sel := rpc.Selector("slot0()")
	liquiditySel := rpc.Selector("liquidity()")
	spacingSel := rpc.Selector("tickSpacing()")

	// Pass one: three reads per candidate, interleaved so the results stay
	// trivially indexable.
	calls := make([]rpc.Call, 0, len(candidates)*3)
	for _, c := range candidates {
		calls = append(calls,
			rpc.Call{Target: c.address, CallData: slot0Sel},
			rpc.Call{Target: c.address, CallData: liquiditySel},
			rpc.Call{Target: c.address, CallData: spacingSel},
		)
	}

	results, err := u.mc.Aggregate(ctx, calls)
	if err != nil {
		return nil, fmt.Errorf("onchain: uniswap v3 slot0: %w", err)
	}
	if len(results) != len(calls) {
		return nil, fmt.Errorf("onchain: uniswap v3 got %d results for %d calls", len(results), len(calls))
	}

	type live struct {
		v3Candidate
		sqrtPriceX96 *big.Int
		tick         int32
		liquidity    *big.Int
		tickSpacing  int32
	}

	var found []live

	for i, c := range candidates {
		slot0Res := results[i*3]
		liqRes := results[i*3+1]
		spacingRes := results[i*3+2]

		if !slot0Res.Success || !liqRes.Success {
			// No pool at that address for this tier, which is the usual case.
			continue
		}

		sqrtPrice, tick, err := decodeSlot0(slot0Res.ReturnData)
		if err != nil {
			continue
		}
		if sqrtPrice.Sign() <= 0 {
			continue
		}

		liquidity := new(big.Int).SetBytes(liqRes.ReturnData)
		if liquidity.Sign() <= 0 {
			// Deployed but holding no active liquidity, so it cannot quote.
			continue
		}

		spacing := int32(60)
		if spacingRes.Success {
			if n, err := decodeInt24(spacingRes.ReturnData); err == nil && n != 0 {
				spacing = n
			}
		}

		found = append(found, live{
			v3Candidate:  c,
			sqrtPriceX96: sqrtPrice,
			tick:         tick,
			liquidity:    liquidity,
			tickSpacing:  spacing,
		})
	}

	if len(found) == 0 {
		return nil, nil
	}

	// Pass two: the tick window around each pool's current price.
	tickSel := rpc.Selector("ticks(int24)")

	var (
		tickCalls   []rpc.Call
		tickIndexes [][]int32
	)

	for _, f := range found {
		indexes := windowTicks(f.tick, f.tickSpacing)
		tickIndexes = append(tickIndexes, indexes)

		for _, idx := range indexes {
			arg, err := encodeInt24(idx)
			if err != nil {
				return nil, err
			}
			tickCalls = append(tickCalls, rpc.Call{
				Target:   f.address,
				CallData: append(append([]byte{}, tickSel...), arg...),
			})
		}
	}

	tickResults, err := u.mc.Aggregate(ctx, tickCalls)
	if err != nil {
		return nil, fmt.Errorf("onchain: uniswap v3 ticks: %w", err)
	}

	out := make([]dex.Pool, 0, len(found))
	cursor := 0

	for i, f := range found {
		indexes := tickIndexes[i]

		var ticks []amm.V3Tick
		for _, idx := range indexes {
			if cursor >= len(tickResults) {
				break
			}
			r := tickResults[cursor]
			cursor++

			if !r.Success {
				continue
			}
			net, initialized, err := decodeTick(r.ReturnData)
			if err != nil || !initialized || net.Sign() == 0 {
				continue
			}
			ticks = append(ticks, amm.V3Tick{Index: idx, LiquidityNet: net})
		}

		if len(ticks) == 0 {
			// With no tick data the pool can only be quoted inside its current
			// range, and any trade that would leave it errors. That is still
			// useful for small trades, so keep it.
			ticks = nil
		}

		out = append(out, dex.Pool{
			Address:      f.address,
			Protocol:     dex.UniswapV3,
			Token0:       f.token0,
			Token1:       f.token1,
			FeeBps:       f.feeBps,
			SqrtPriceX96: f.sqrtPriceX96,
			Liquidity:    f.liquidity,
			Tick:         f.tick,
			TickSpacing:  f.tickSpacing,
			Ticks:        ticks,
		})
	}

	return out, nil
}

func (u *UniswapV3) candidates() ([]v3Candidate, error) {
	pairs := pairsOf(u.tokens)

	var out []v3Candidate
	for _, p := range pairs {
		t0, t1 := orderTokens(p[0], p[1])
		for _, fee := range u.feeTiers {
			addr, err := v3PoolAddress(t0, t1, fee)
			if err != nil {
				return nil, err
			}
			out = append(out, v3Candidate{address: addr, token0: t0, token1: t1, feeBps: fee})
		}
	}
	return out, nil
}

// windowTicks returns the tick indexes to sample around current.
func windowTicks(current, spacing int32) []int32 {
	if spacing <= 0 {
		spacing = 60
	}

	// Snap to a multiple of the spacing; only those can be initialised.
	base := current - (current % spacing)

	out := make([]int32, 0, tickWindow*2)
	for i := tickWindow; i >= 1; i-- {
		idx := base - int32(i)*tickStride*spacing
		if idx >= amm.MinTick {
			out = append(out, idx)
		}
	}
	for i := 1; i <= tickWindow; i++ {
		idx := base + int32(i)*tickStride*spacing
		if idx <= amm.MaxTick {
			out = append(out, idx)
		}
	}
	return out
}

// decodeSlot0 reads slot0() returns (uint160 sqrtPriceX96, int24 tick, ...).
func decodeSlot0(data []byte) (*big.Int, int32, error) {
	if len(data) < 2*32 {
		return nil, 0, rpc.ErrShortReturn
	}

	sqrtPrice := new(big.Int).SetBytes(data[0:32])

	tick, err := decodeInt24(data[32:64])
	if err != nil {
		return nil, 0, err
	}
	return sqrtPrice, tick, nil
}

// decodeTick reads ticks(int24) returns (uint128 liquidityGross,
// int128 liquidityNet, ...) and the initialized flag at the end.
func decodeTick(data []byte) (*big.Int, bool, error) {
	// liquidityGross, liquidityNet, feeGrowthOutside0, feeGrowthOutside1,
	// tickCumulativeOutside, secondsPerLiquidityOutside, secondsOutside,
	// initialized.
	const words = 8
	if len(data) < words*32 {
		return nil, false, rpc.ErrShortReturn
	}

	net := new(big.Int).SetBytes(data[32:64])

	// liquidityNet is int128 and is routinely negative at the upper bound of a
	// position. Sign-extend it back.
	signBit := new(big.Int).Lsh(big.NewInt(1), 127)
	if net.Cmp(signBit) >= 0 {
		net.Sub(net, new(big.Int).Lsh(big.NewInt(1), 128))
	}

	initialized := new(big.Int).SetBytes(data[7*32:8*32]).Sign() != 0
	return net, initialized, nil
}

// decodeInt24 reads a sign-extended int24 from one word.
func decodeInt24(data []byte) (int32, error) {
	if len(data) < 32 {
		return 0, rpc.ErrShortReturn
	}

	n := new(big.Int).SetBytes(data[:32])

	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 24), big.NewInt(1))
	n.And(n, mask)

	signBit := new(big.Int).Lsh(big.NewInt(1), 23)
	if n.Cmp(signBit) >= 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), 24))
	}
	return int32(n.Int64()), nil
}

// encodeInt24 encodes a tick index as a sign-extended word.
func encodeInt24(tick int32) ([]byte, error) {
	if tick < amm.MinTick || tick > amm.MaxTick {
		return nil, fmt.Errorf("onchain: tick %d out of range", tick)
	}

	n := big.NewInt(int64(tick))
	if n.Sign() < 0 {
		// Two's complement across the full word, which is how the ABI carries
		// a negative int24.
		n.Add(n, new(big.Int).Lsh(big.NewInt(1), 256))
	}

	out := make([]byte, 32)
	b := n.Bytes()
	copy(out[32-len(b):], b)
	return out, nil
}
