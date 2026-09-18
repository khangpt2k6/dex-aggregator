package onchain

import (
	"context"
	"fmt"
	"math/big"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/rpc"
)

// sushiswapFeeBps is fixed at 30 bps for every V2 pair. There are no tiers.
const sushiswapFeeBps = 30

// SushiswapV2 reads constant product pairs.
type SushiswapV2 struct {
	mc     *rpc.Multicaller
	tokens []dex.Token
}

// NewSushiswapV2 returns a source covering every pair of the given tokens.
func NewSushiswapV2(mc *rpc.Multicaller, tokens []dex.Token) *SushiswapV2 {
	return &SushiswapV2{mc: mc, tokens: tokens}
}

// Name identifies the source in logs and in the pools endpoint.
func (s *SushiswapV2) Name() string { return "sushiswap-v2" }

// Pools reads reserves for every derived pair address.
//
// One getReserves call per candidate pair, all of them in one batched request.
// Pairs that do not exist revert and are dropped.
func (s *SushiswapV2) Pools(ctx context.Context) ([]dex.Pool, error) {
	pairs := pairsOf(s.tokens)
	if len(pairs) == 0 {
		return nil, nil
	}

	type candidate struct {
		address string
		token0  dex.Token
		token1  dex.Token
	}

	var (
		candidates []candidate
		calls      []rpc.Call
	)

	getReserves := rpc.Selector("getReserves()")

	for _, p := range pairs {
		addr, err := v2PairAddress(p[0], p[1])
		if err != nil {
			return nil, err
		}

		t0, t1 := orderTokens(p[0], p[1])
		candidates = append(candidates, candidate{address: addr, token0: t0, token1: t1})
		calls = append(calls, rpc.Call{Target: addr, CallData: getReserves})
	}

	results, err := s.mc.Aggregate(ctx, calls)
	if err != nil {
		return nil, fmt.Errorf("onchain: sushiswap getReserves: %w", err)
	}
	if len(results) != len(candidates) {
		return nil, fmt.Errorf("onchain: sushiswap got %d results for %d pairs", len(results), len(candidates))
	}

	out := make([]dex.Pool, 0, len(candidates))
	for i, r := range results {
		if !r.Success {
			// No pair deployed at that address, which is the expected outcome
			// for most token combinations.
			continue
		}

		r0, r1, err := decodeReserves(r.ReturnData)
		if err != nil {
			continue
		}
		if r0.Sign() <= 0 || r1.Sign() <= 0 {
			// A deployed but empty pair cannot quote anything.
			continue
		}

		c := candidates[i]
		out = append(out, dex.Pool{
			Address:  c.address,
			Protocol: dex.SushiswapV2,
			Token0:   c.token0,
			Token1:   c.token1,
			FeeBps:   sushiswapFeeBps,
			Reserve0: r0,
			Reserve1: r1,
		})
	}

	return out, nil
}

// decodeReserves reads getReserves() returns (uint112, uint112, uint32).
//
// The reserves are uint112 but arrive padded to full words, so they read as
// ordinary unsigned integers. The trailing block timestamp is ignored.
func decodeReserves(data []byte) (*big.Int, *big.Int, error) {
	if len(data) < 3*32 {
		return nil, nil, rpc.ErrShortReturn
	}
	r0 := new(big.Int).SetBytes(data[0:32])
	r1 := new(big.Int).SetBytes(data[32:64])
	return r0, r1, nil
}
