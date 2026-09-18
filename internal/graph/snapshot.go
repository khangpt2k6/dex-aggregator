// Package graph turns a set of pools into a routable token graph and searches
// it for the route that maximises output.
//
// A Snapshot is immutable once built. The indexer builds a new one on every
// refresh and publishes it with a single atomic pointer store, so readers never
// take a lock and never observe a half-built graph.
package graph

import (
	"math/big"
	"strings"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

// Edge is one directed trading opportunity: sell From, receive To, through Pool.
type Edge struct {
	Pool *dex.Pool
	From string
	To   string
}

// edgeRef is the internal form of an edge, using integer token indices instead
// of address strings.
//
// The router visits every edge on every relaxation round, so the difference
// between comparing two ints and hashing two 42-character strings is the
// difference between hitting the latency budget and missing it.
type edgeRef struct {
	pool int
	from int
	to   int
}

// Snapshot is an immutable view of the routable graph at a point in time.
type Snapshot struct {
	tokens []dex.Token
	index  map[string]int
	pools  []dex.Pool

	// adj[i] holds every edge leaving token i.
	adj [][]edgeRef

	edges   int
	builtAt time.Time
}

// BuildSnapshot indexes pools into a routable graph.
//
// Pools whose on-chain state has not loaded, or whose liquidity is zero, are
// dropped here rather than at query time. Carrying them would make the router
// pay for edges that can only return an error, on every round, for every
// request.
func BuildSnapshot(pools []dex.Pool, at time.Time) *Snapshot {
	s := &Snapshot{
		index:   make(map[string]int),
		builtAt: at,
	}

	for _, p := range pools {
		if !usable(&p) {
			continue
		}
		if !prepare(&p) {
			continue
		}

		s.pools = append(s.pools, p)
		poolIdx := len(s.pools) - 1

		a := s.intern(p.Token0)
		b := s.intern(p.Token1)

		s.addEdge(edgeRef{pool: poolIdx, from: a, to: b})
		s.addEdge(edgeRef{pool: poolIdx, from: b, to: a})
	}

	return s
}

// usable reports whether a pool has enough loaded state to quote a swap.
func usable(p *dex.Pool) bool {
	if p.Token0.Address == "" || p.Token1.Address == "" {
		return false
	}
	if strings.EqualFold(p.Token0.Address, p.Token1.Address) {
		return false
	}

	switch p.Protocol {
	case dex.SushiswapV2:
		return positive(p.Reserve0) && positive(p.Reserve1)
	case dex.UniswapV3:
		return positive(p.SqrtPriceX96) && positive(p.Liquidity)
	default:
		return false
	}
}

func positive(n *big.Int) bool { return n != nil && n.Sign() > 0 }

// prepare does the per-pool setup that would otherwise be repeated on every
// quote, and reports whether the pool is still usable afterwards.
//
// The router asks a V3 pool for a quote on every relaxation round of every
// request. Sorting its ticks and deriving their sqrt ratios there would mean
// doing identical work thousands of times per second for state that only
// changes once per refresh. Doing it here moves that cost off the hot path
// entirely, which is most of what buys the latency budget.
func prepare(p *dex.Pool) bool {
	if p.Protocol != dex.UniswapV3 {
		return true
	}

	ticks, err := amm.PrepareTicks(p.Ticks)
	if err != nil {
		// A tick outside protocol bounds means the pool state is corrupt.
		// Dropping it is better than letting it fail every quote.
		return false
	}
	p.Ticks = ticks
	return true
}

// intern returns the index of a token, adding it to the graph if new.
func (s *Snapshot) intern(t dex.Token) int {
	key := normalize(t.Address)
	if i, ok := s.index[key]; ok {
		return i
	}

	s.tokens = append(s.tokens, t)
	i := len(s.tokens) - 1
	s.index[key] = i
	s.adj = append(s.adj, nil)
	return i
}

func (s *Snapshot) addEdge(e edgeRef) {
	s.adj[e.from] = append(s.adj[e.from], e)
	s.edges++
}

// TokenCount returns how many distinct tokens the graph covers.
func (s *Snapshot) TokenCount() int { return len(s.tokens) }

// EdgeCount returns how many directed edges the graph holds. Each usable pool
// contributes two.
func (s *Snapshot) EdgeCount() int { return s.edges }

// BuiltAt returns when the snapshot was assembled. Handlers report this as
// staleness so a caller can judge how much to trust a quote.
func (s *Snapshot) BuiltAt() time.Time { return s.builtAt }

// Age returns how long ago the snapshot was built.
func (s *Snapshot) Age() time.Duration { return time.Since(s.builtAt) }

// Pools returns a copy of the indexed pools.
func (s *Snapshot) Pools() []dex.Pool {
	out := make([]dex.Pool, len(s.pools))
	copy(out, s.pools)
	return out
}

// Tokens returns a copy of the tokens in the graph.
func (s *Snapshot) Tokens() []dex.Token {
	out := make([]dex.Token, len(s.tokens))
	copy(out, s.tokens)
	return out
}

// Token looks up a token by address, ignoring case.
func (s *Snapshot) Token(address string) (dex.Token, bool) {
	i, ok := s.index[normalize(address)]
	if !ok {
		return dex.Token{}, false
	}
	return s.tokens[i], true
}

// EdgesFrom returns the edges leaving a token.
//
// The result is a fresh slice: a snapshot serves every concurrent request, so
// handing out the backing array would let one caller corrupt routing for all
// of them. The router does not use this method; it walks the integer-indexed
// adjacency directly.
func (s *Snapshot) EdgesFrom(address string) []Edge {
	i, ok := s.index[normalize(address)]
	if !ok {
		return nil
	}

	refs := s.adj[i]
	out := make([]Edge, 0, len(refs))
	for _, r := range refs {
		out = append(out, Edge{
			Pool: &s.pools[r.pool],
			From: s.tokens[r.from].Address,
			To:   s.tokens[r.to].Address,
		})
	}
	return out
}

// tokenIndex resolves an address to its integer index for the router.
func (s *Snapshot) tokenIndex(address string) (int, bool) {
	i, ok := s.index[normalize(address)]
	return i, ok
}

// normalize canonicalises an address for map lookup. Addresses arrive
// checksummed from token lists, lowercase from RPC, and arbitrary from users.
func normalize(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
