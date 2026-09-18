// Package sim generates deterministic pool state so the aggregator runs with
// no RPC endpoint and no API key.
//
// This is not a toy mode bolted on for tests. A reader who clones the
// repository should be able to start it and get a real multi-hop route back,
// and a reviewer should get the same numbers the README quotes. Requiring an
// Alchemy key to see anything work would make the project unrunnable for most
// of the people looking at it.
//
// The state is synthetic but structurally honest: real mainnet token addresses,
// prices seeded from plausible USD values, liquidity concentrated the way a real
// V3 position is, and the same hub-and-spoke pair topology mainnet actually has.
// Every pool it produces is flagged Simulated, and that flag travels all the way
// to the UI.
package sim

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"sort"
	"strings"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

// Source produces simulated pools.
type Source struct {
	seed int64
}

// New returns a source that generates identical state for identical seeds.
func New(seed int64) *Source { return &Source{seed: seed} }

// Name identifies the source in logs and in the pools endpoint.
func (s *Source) Name() string { return "simulated" }

// usdPrices anchor the generated exchange rates. Routing only cares about
// ratios, but plausible absolute values make a quote readable at a glance and
// make an obviously broken swap obviously broken.
var usdPrices = map[string]float64{
	"WETH": 3000,
	"USDC": 1,
	"USDT": 1,
	"DAI":  1,
	"WBTC": 60000,
	"LINK": 15,
	"UNI":  8,
	"AAVE": 250,
}

// venue describes one pool to generate for a pair.
type venue struct {
	protocol dex.Protocol
	feeBps   uint32
	tvlUSD   float64
}

// Pools returns the generated pool set.
//
// It takes a context to satisfy dex.PoolSource, but does no I/O, so the only
// thing the context can do is cancel before work starts.
func (s *Source) Pools(ctx context.Context) ([]dex.Pool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rng := rand.New(rand.NewSource(s.seed))
	tokens := dex.Tokens()

	pairs := pairsToGenerate(tokens)

	var pools []dex.Pool
	for _, pr := range pairs {
		for _, v := range venuesFor(rng) {
			p, err := s.generate(rng, len(pools), pr.a, pr.b, v)
			if err != nil {
				return nil, err
			}
			pools = append(pools, p)
		}
	}

	return pools, nil
}

type pair struct{ a, b dex.Token }

// pairsToGenerate mirrors how mainnet liquidity is actually shaped: almost
// everything is paired against WETH or USDC, and those two are the hubs that
// multi-hop routes pass through. Generating a complete graph instead would make
// routing easy in a way real markets are not.
func pairsToGenerate(tokens []dex.Token) []pair {
	bySymbol := map[string]dex.Token{}
	for _, t := range tokens {
		bySymbol[t.Symbol] = t
	}

	weth := bySymbol["WETH"]
	usdc := bySymbol["USDC"]

	var out []pair
	seen := map[string]bool{}

	add := func(a, b dex.Token) {
		if a.Address == "" || b.Address == "" || strings.EqualFold(a.Address, b.Address) {
			return
		}
		key := pairKey(a, b)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, pair{a: a, b: b})
	}

	for _, t := range tokens {
		add(weth, t)
	}
	for _, t := range tokens {
		add(usdc, t)
	}

	// A couple of direct stable pairs, which is where the tight-spread routes
	// come from and where an arbitrage loop can appear.
	add(bySymbol["USDC"], bySymbol["USDT"])
	add(bySymbol["DAI"], bySymbol["USDT"])
	add(bySymbol["WBTC"], bySymbol["DAI"])

	sort.Slice(out, func(i, j int) bool { return pairKey(out[i].a, out[i].b) < pairKey(out[j].a, out[j].b) })
	return out
}

func pairKey(a, b dex.Token) string {
	x, y := strings.ToLower(a.Symbol), strings.ToLower(b.Symbol)
	if x > y {
		x, y = y, x
	}
	return x + "/" + y
}

// venuesFor decides which pools exist for a pair. Competing venues at different
// fee tiers and depths are what give the router something to choose between.
func venuesFor(rng *rand.Rand) []venue {
	base := 2_000_000 + rng.Float64()*20_000_000

	vs := []venue{
		{protocol: dex.UniswapV3, feeBps: 30, tvlUSD: base},
		{protocol: dex.SushiswapV2, feeBps: 30, tvlUSD: base * (0.2 + rng.Float64()*0.6)},
	}

	// Sometimes a tight fee tier with less depth, which is the case where the
	// best pool depends on trade size.
	if rng.Intn(10) < 6 {
		vs = append(vs, venue{protocol: dex.UniswapV3, feeBps: 5, tvlUSD: base * (0.05 + rng.Float64()*0.25)})
	}
	return vs
}

func (s *Source) generate(rng *rand.Rand, idx int, a, b dex.Token, v venue) (dex.Pool, error) {
	// Order tokens the way a factory would, so token0 is deterministic.
	t0, t1 := a, b
	if strings.ToLower(t0.Address) > strings.ToLower(t1.Address) {
		t0, t1 = t1, t0
	}

	price0 := usdPrices[t0.Symbol]
	price1 := usdPrices[t1.Symbol]
	if price0 <= 0 || price1 <= 0 {
		return dex.Pool{}, fmt.Errorf("sim: no reference price for %s/%s", t0.Symbol, t1.Symbol)
	}

	// Per-pool dispersion of up to 0.4% either way. Without it every venue
	// quotes the same rate and the router has nothing to decide.
	price1 *= 1 + (rng.Float64()-0.5)*0.008

	pool := dex.Pool{
		Address:   fmt.Sprintf("0xsim%037x", idx),
		Protocol:  v.protocol,
		Token0:    t0,
		Token1:    t1,
		FeeBps:    v.feeBps,
		Simulated: true,
	}

	switch v.protocol {
	case dex.SushiswapV2:
		// Half the value on each side, which is what a balanced pool holds.
		pool.Reserve0 = rawAmount(v.tvlUSD/2/price0, t0.Decimals)
		pool.Reserve1 = rawAmount(v.tvlUSD/2/price1, t1.Decimals)

	case dex.UniswapV3:
		if err := fillV3(&pool, price0, price1, v.tvlUSD); err != nil {
			return dex.Pool{}, err
		}
	}

	return pool, nil
}

// fillV3 sets the concentrated liquidity state for a pool at the given prices.
func fillV3(p *dex.Pool, price0, price1, tvlUSD float64) error {
	// A V3 price is the ratio of raw token amounts, so the decimal difference
	// between the two tokens is part of the price, not a display detail.
	rawPrice := (price0 / price1) * math.Pow(10, float64(p.Token1.Decimals)-float64(p.Token0.Decimals))

	const spacing int32 = 60

	tick := int32(math.Round(math.Log(rawPrice) / math.Log(1.0001)))
	tick -= tick % spacing
	if tick < amm.MinTick {
		tick = amm.MinTick - amm.MinTick%spacing
	}
	if tick > amm.MaxTick {
		tick = amm.MaxTick - amm.MaxTick%spacing
	}

	sqrtP, err := amm.GetSqrtRatioAtTick(tick)
	if err != nil {
		return fmt.Errorf("sim: tick %d for %s/%s: %w", tick, p.Token0.Symbol, p.Token1.Symbol, err)
	}

	// Virtual token1 reserve is L * sqrtP / 2^96, so to hold half the pool
	// value in token1: L = (raw token1 amount << 96) / sqrtPriceX96.
	y := rawAmount(tvlUSD/2/price1, p.Token1.Decimals)
	liquidity := new(big.Int).Lsh(y, 96)
	liquidity.Div(liquidity, sqrtP)
	if liquidity.Sign() <= 0 {
		liquidity = big.NewInt(1)
	}

	p.SqrtPriceX96 = sqrtP
	p.Liquidity = liquidity
	p.Tick = tick
	p.TickSpacing = spacing
	p.Ticks = buildTicks(tick, spacing, liquidity)
	return nil
}

// buildTicks lays out a liquidity distribution around the current price.
//
// Real concentrated positions cluster near the current price and thin out
// away from it, which is exactly why a large trade gets a worse rate than a
// small one. The window is wide enough that ordinary trades stay inside it,
// and a trade that runs past the end is reported as unquotable rather than
// extrapolated.
func buildTicks(current, spacing int32, liquidity *big.Int) []amm.V3Tick {
	const steps = 8
	const stride = 20 // in units of tick spacing, so about 12% of price each

	ticks := make([]amm.V3Tick, 0, steps*2)

	for i := steps; i >= 1; i-- {
		idx := current - int32(i)*stride*spacing
		if idx < amm.MinTick {
			continue
		}
		// Crossing downward subtracts LiquidityNet, so a positive value here
		// means liquidity thins out as price falls.
		ticks = append(ticks, amm.V3Tick{
			Index:        idx,
			LiquidityNet: portion(liquidity, i),
		})
	}

	for i := 1; i <= steps; i++ {
		idx := current + int32(i)*stride*spacing
		if idx > amm.MaxTick {
			continue
		}
		ticks = append(ticks, amm.V3Tick{
			Index:        idx,
			LiquidityNet: new(big.Int).Neg(portion(liquidity, i)),
		})
	}

	return ticks
}

// portion returns a share of liquidity that shrinks with distance from the
// current price.
func portion(liquidity *big.Int, distance int) *big.Int {
	out := new(big.Int).Div(liquidity, big.NewInt(int64(distance)*4))
	if out.Sign() <= 0 {
		out = big.NewInt(1)
	}
	return out
}

// rawAmount converts a human-readable token amount to its on-chain integer
// representation at the token's decimals.
func rawAmount(units float64, decimals uint8) *big.Int {
	if units < 0 {
		units = 0
	}

	// Go through big.Float so that large values at 18 decimals do not lose
	// precision to float64's 53-bit mantissa.
	scale := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil))
	f := new(big.Float).SetFloat64(units)
	f.Mul(f, scale)

	out, _ := f.Int(nil)
	if out.Sign() <= 0 {
		out = big.NewInt(1)
	}
	return out
}
