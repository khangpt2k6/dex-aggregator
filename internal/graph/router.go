package graph

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

var (
	// ErrNoRoute is returned when no path of the allowed length connects the
	// two tokens with quotable liquidity.
	ErrNoRoute = errors.New("graph: no route found")

	// ErrUnknownToken is returned when a token is not present in the snapshot.
	ErrUnknownToken = errors.New("graph: token not in graph")

	// ErrBadRequest is returned for a nonsensical query.
	ErrBadRequest = errors.New("graph: invalid route request")
)

// Hop is one swap in a route.
type Hop struct {
	PoolAddress    string       `json:"poolAddress"`
	Protocol       dex.Protocol `json:"protocol"`
	FeeBps         uint32       `json:"feeBps"`
	TokenIn        string       `json:"tokenIn"`
	TokenOut       string       `json:"tokenOut"`
	TokenInSymbol  string       `json:"tokenInSymbol"`
	TokenOutSymbol string       `json:"tokenOutSymbol"`
	AmountIn       *big.Int     `json:"-"`
	AmountOut      *big.Int     `json:"-"`
}

// Route is a complete path from one token to another.
type Route struct {
	Hops      []Hop
	AmountIn  *big.Int
	AmountOut *big.Int

	// PriceImpactBps is how far the realised rate falls short of the marginal
	// rate along this same path, in basis points. It is a property of the
	// trade size, not of the market moving.
	PriceImpactBps int64
}

// FindBestRoute returns the path from tokenIn to tokenOut that produces the
// most tokenOut for the given amountIn.
//
// # Why this is not Dijkstra
//
// The obvious formulation weights each pool by its spot price and runs a
// shortest-path search. That is wrong for AMMs. Spot price is the marginal
// price at zero size, but execution price is a non-linear function of size: a
// pool with an excellent quoted price and shallow liquidity is the worst
// possible venue for a large trade. A router built on static spot-price
// weights returns the wrong pool exactly when the trade is big enough for the
// answer to matter.
//
// The correct edge weight is -log(amountOut/amountIn) evaluated at the amount
// actually arriving at that edge. Maximising final output is then the same as
// minimising the sum of weights, since log turns the product of per-hop rates
// into a sum. Two things follow. The weights are not known until the amount is
// known, so the search must carry real token amounts rather than prices, which
// rules out Dijkstra. And weights go negative whenever a hop gains value, so
// the search must tolerate negative edges, which is what Bellman-Ford does.
//
// # The algorithm
//
// This is a Bellman-Ford relaxation indexed by hop count. Round k computes,
// for every token, the largest amount reachable using exactly k hops. Carrying
// only the best amount per (hops, token) is optimal because every edge
// function is monotonically increasing: if one path delivers more of a token
// than another, it also delivers more after any continuation. That is the
// optimal-substructure argument Bellman-Ford needs, and it survives the
// non-linearity that breaks the spot-price formulation.
//
// The search runs entirely over the in-memory snapshot. It takes no lock,
// makes no network call, and does not mutate the snapshot, so any number of
// requests can run against one snapshot concurrently.
func FindBestRoute(s *Snapshot, tokenIn, tokenOut string, amountIn *big.Int, maxHops int) (*Route, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil snapshot", ErrBadRequest)
	}
	if amountIn == nil || amountIn.Sign() <= 0 {
		return nil, fmt.Errorf("%w: amountIn must be positive", ErrBadRequest)
	}
	if maxHops < 1 {
		return nil, fmt.Errorf("%w: maxHops must be at least 1", ErrBadRequest)
	}

	src, ok := s.tokenIndex(tokenIn)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownToken, tokenIn)
	}
	dst, ok := s.tokenIndex(tokenOut)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownToken, tokenOut)
	}
	if src == dst {
		return nil, fmt.Errorf("%w: tokenIn and tokenOut are the same", ErrBadRequest)
	}

	table := s.relax(src, amountIn, maxHops)

	bestRound, ok := table.bestRoundFor(dst)
	if !ok {
		return nil, fmt.Errorf("%w: %s to %s within %d hops", ErrNoRoute, tokenIn, tokenOut, maxHops)
	}

	hops := s.reconstruct(table, dst, bestRound)
	out := table.amount[bestRound][dst]

	route := &Route{
		Hops:      hops,
		AmountIn:  new(big.Int).Set(amountIn),
		AmountOut: new(big.Int).Set(out),
	}
	route.PriceImpactBps = s.priceImpact(hops, amountIn, out)
	return route, nil
}

// relaxTable holds the dynamic programming state.
//
// amount[k][i] is the largest amount of token i reachable in exactly k hops,
// or nil if unreachable. from[k][i] records the edge that achieved it.
type relaxTable struct {
	amount [][]*big.Int
	from   [][]edgeRef
	seen   [][]bool
	rounds int
}

func (t *relaxTable) bestRoundFor(token int) (int, bool) {
	best := -1
	for k := 1; k <= t.rounds; k++ {
		if !t.seen[k][token] {
			continue
		}
		if best == -1 || t.amount[k][token].Cmp(t.amount[best][token]) > 0 {
			best = k
		}
	}
	return best, best != -1
}

// relax runs the hop-indexed Bellman-Ford relaxation.
func (s *Snapshot) relax(src int, amountIn *big.Int, maxHops int) *relaxTable {
	n := len(s.tokens)

	t := &relaxTable{
		amount: make([][]*big.Int, maxHops+1),
		from:   make([][]edgeRef, maxHops+1),
		seen:   make([][]bool, maxHops+1),
		rounds: maxHops,
	}
	for k := 0; k <= maxHops; k++ {
		t.amount[k] = make([]*big.Int, n)
		t.from[k] = make([]edgeRef, n)
		t.seen[k] = make([]bool, n)
	}

	t.amount[0][src] = amountIn
	t.seen[0][src] = true

	// One workspace for the whole query. Every candidate swap below reuses it
	// rather than allocating fresh big.Int backing arrays, which is what keeps
	// garbage collection out of the latency tail.
	sc := amm.NewScratch()

	for k := 1; k <= maxHops; k++ {
		for i := 0; i < n; i++ {
			if !t.seen[k-1][i] {
				continue
			}
			in := t.amount[k-1][i]
			fromAddr := s.tokens[i].Address

			for _, e := range s.adj[i] {
				out, err := s.pools[e.pool].AmountOutInto(sc, in, fromAddr)
				if err != nil {
					// A pool that cannot quote this size is not a reason to
					// fail the request. Skip the edge and keep searching.
					continue
				}
				if out.Sign() <= 0 {
					continue
				}
				// out points into the workspace, so compare before copying.
				// Most candidates lose, and a loser costs no allocation.
				if t.seen[k][e.to] && t.amount[k][e.to].Cmp(out) >= 0 {
					continue
				}

				if t.seen[k][e.to] {
					t.amount[k][e.to].Set(out)
				} else {
					t.amount[k][e.to] = new(big.Int).Set(out)
				}
				t.from[k][e.to] = e
				t.seen[k][e.to] = true
			}
		}
	}

	return t
}

// reconstruct walks the predecessor edges back from the destination.
func (s *Snapshot) reconstruct(t *relaxTable, dst, rounds int) []Hop {
	hops := make([]Hop, rounds)

	token := dst
	for k := rounds; k >= 1; k-- {
		e := t.from[k][token]
		pool := &s.pools[e.pool]

		hops[k-1] = Hop{
			PoolAddress:    pool.Address,
			Protocol:       pool.Protocol,
			FeeBps:         pool.FeeBps,
			TokenIn:        s.tokens[e.from].Address,
			TokenOut:       s.tokens[e.to].Address,
			TokenInSymbol:  s.tokens[e.from].Symbol,
			TokenOutSymbol: s.tokens[e.to].Symbol,
			AmountIn:       new(big.Int).Set(t.amount[k-1][e.from]),
			AmountOut:      new(big.Int).Set(t.amount[k][e.to]),
		}
		token = e.from
	}

	return hops
}

// priceImpact measures how much worse the realised rate is than the marginal
// rate along the same path, in basis points.
//
// It re-prices the identical path at a small probe size. The probe's rate is
// close to marginal because a small trade barely moves any pool, so scaling it
// up to the real size gives what the trade would have produced with no impact.
// The shortfall against that is the impact.
//
// Comparing against the same path rather than against a global best price
// matters: this reports the cost of size, not the cost of routing.
func (s *Snapshot) priceImpact(hops []Hop, amountIn, amountOut *big.Int) int64 {
	if len(hops) == 0 || amountIn.Sign() <= 0 {
		return 0
	}

	// One thousandth of the trade, floored at 1 so the probe is never zero.
	probeIn := new(big.Int).Div(amountIn, big.NewInt(1000))
	if probeIn.Sign() <= 0 {
		probeIn = big.NewInt(1)
	}

	probeOut := new(big.Int).Set(probeIn)
	for _, h := range hops {
		pool, ok := s.poolByAddress(h.PoolAddress)
		if !ok {
			return 0
		}
		next, err := pool.AmountOut(probeOut, h.TokenIn)
		if err != nil || next.Sign() <= 0 {
			return 0
		}
		probeOut = next
	}

	// What the full trade would yield at the probe's rate.
	expected := new(big.Int).Mul(probeOut, amountIn)
	expected.Div(expected, probeIn)
	if expected.Sign() <= 0 {
		return 0
	}

	shortfall := new(big.Int).Sub(expected, amountOut)
	if shortfall.Sign() <= 0 {
		// Rounding can make a tiny probe look marginally worse than the real
		// trade. Report no impact rather than a negative one.
		return 0
	}

	shortfall.Mul(shortfall, big.NewInt(bpsScale))
	shortfall.Div(shortfall, expected)

	if !shortfall.IsInt64() {
		return bpsScale
	}
	return shortfall.Int64()
}

const bpsScale = 10000

// poolByAddress finds an indexed pool. Routes are short, so the linear scan
// costs less than maintaining another map.
func (s *Snapshot) poolByAddress(address string) (*dex.Pool, bool) {
	for i := range s.pools {
		if s.pools[i].Address == address {
			return &s.pools[i], true
		}
	}
	return nil, false
}
