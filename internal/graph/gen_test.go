package graph

import (
	"fmt"
	"math/big"
	"math/rand"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

// generateSnapshot builds a graph of roughly realistic shape for benchmarking:
// a connected backbone so every token is reachable, plus extra pools so that
// popular pairs have several competing venues, which is what makes the router
// do real work.
//
// It is deterministic for a given seed, so a latency regression is a change in
// the code and never a change in the data.
func generateSnapshot(tokenCount, poolCount int, seed int64) *Snapshot {
	rng := rand.New(rand.NewSource(seed))

	tokens := make([]dex.Token, tokenCount)
	decimals := []uint8{18, 6, 8, 18}
	for i := range tokens {
		tokens[i] = dex.Token{
			Address:  fmt.Sprintf("0x%040x", i+1),
			Symbol:   fmt.Sprintf("T%02d", i),
			Decimals: decimals[i%len(decimals)],
		}
	}

	pools := make([]dex.Pool, 0, poolCount)

	// Backbone: attach each token to one already-connected token, so the graph
	// has no unreachable island and every routing query has an answer.
	for i := 1; i < tokenCount; i++ {
		j := rng.Intn(i)
		pools = append(pools, genPool(rng, len(pools), tokens[j], tokens[i]))
	}

	// Extra venues, including duplicate pairs on different protocols.
	for len(pools) < poolCount {
		a := rng.Intn(tokenCount)
		b := rng.Intn(tokenCount)
		if a == b {
			continue
		}
		pools = append(pools, genPool(rng, len(pools), tokens[a], tokens[b]))
	}

	return BuildSnapshot(pools, time.Now())
}

func genPool(rng *rand.Rand, idx int, a, b dex.Token) dex.Pool {
	// Roughly 40% concentrated liquidity, matching the rough split of where
	// volume sits between the two protocols.
	if rng.Intn(10) < 4 {
		return genV3(rng, idx, a, b)
	}
	return genV2(rng, idx, a, b)
}

func genV2(rng *rand.Rand, idx int, a, b dex.Token) dex.Pool {
	return dex.Pool{
		Address:  fmt.Sprintf("v2-%04d", idx),
		Protocol: dex.SushiswapV2,
		Token0:   a,
		Token1:   b,
		FeeBps:   30,
		Reserve0: reserve(rng, a),
		Reserve1: reserve(rng, b),
	}
}

func genV3(rng *rand.Rand, idx int, a, b dex.Token) dex.Pool {
	tick := int32(rng.Intn(20000) - 10000)
	tick -= tick % 60

	sqrtP, err := amm.GetSqrtRatioAtTick(tick)
	if err != nil {
		panic(err)
	}

	liq := new(big.Int).Mul(big.NewInt(int64(1+rng.Intn(5_000_000))), pow10(18))

	// A window of initialised ticks on both sides of the current price, which
	// is what a real fetch would return.
	ticks := make([]amm.V3Tick, 0, 12)
	for step := 6; step >= 1; step-- {
		ticks = append(ticks, amm.V3Tick{
			Index:        tick - int32(step*600),
			LiquidityNet: new(big.Int).Div(liq, big.NewInt(int64(step+1))),
		})
	}
	for step := 1; step <= 6; step++ {
		ticks = append(ticks, amm.V3Tick{
			Index:        tick + int32(step*600),
			LiquidityNet: new(big.Int).Neg(new(big.Int).Div(liq, big.NewInt(int64(step+1)))),
		})
	}

	feeTiers := []uint32{5, 30, 100}

	return dex.Pool{
		Address:      fmt.Sprintf("v3-%04d", idx),
		Protocol:     dex.UniswapV3,
		Token0:       a,
		Token1:       b,
		FeeBps:       feeTiers[rng.Intn(len(feeTiers))],
		SqrtPriceX96: sqrtP,
		Liquidity:    liq,
		Tick:         tick,
		TickSpacing:  60,
		Ticks:        ticks,
	}
}

// reserve returns a plausible pool reserve, spread over three orders of
// magnitude so that pools genuinely differ in depth and the router has a real
// choice to make.
func reserve(rng *rand.Rand, t dex.Token) *big.Int {
	units := int64(1_000 + rng.Intn(1_000_000))
	return new(big.Int).Mul(big.NewInt(units), pow10(int(t.Decimals)))
}
