package graph

import (
	"math/big"
	"sort"
	"strings"
)

// Cycle is a closed loop that returns more of a token than it consumed.
type Cycle struct {
	// Tokens lists the loop in order, starting and ending on the same token.
	Tokens []string `json:"tokens"`

	// Symbols is Tokens rendered as tickers, for display.
	Symbols []string `json:"symbols"`

	// Pools lists the pool used at each step.
	Pools []string `json:"pools"`

	AmountIn  *big.Int `json:"-"`
	AmountOut *big.Int `json:"-"`

	// ProfitBps is the gain over the input, in basis points, at this size.
	ProfitBps int64 `json:"profitBps"`
}

// FindArbitrage reports closed loops that return more than they consumed.
//
// This costs almost nothing to add. In the -log edge weights the router
// already uses, a loop that gains value is a negative-weight cycle, and
// Bellman-Ford detects those as an ordinary consequence of how it works. The
// implementation below is literally the router's own relaxation, asked whether
// any token can reach itself with more than it started.
//
// probe maps a starting token to the size to test. Size matters here for the
// same reason it matters for routing: an opportunity that clears the fees at
// one size may not clear them at another, so an arbitrage is only ever real
// with respect to an amount.
//
// Results are deduplicated by pool set, because the same loop found from three
// different starting tokens is one opportunity, not three, and sorted with the
// most profitable first.
func FindArbitrage(s *Snapshot, probe map[string]*big.Int, maxHops int) []Cycle {
	if s == nil {
		return nil
	}
	// A cycle needs at least two hops out and back.
	if len(probe) == 0 || maxHops < 2 {
		return []Cycle{}
	}

	var found []Cycle
	seen := map[string]bool{}

	for address, amount := range probe {
		if amount == nil || amount.Sign() <= 0 {
			continue
		}
		src, ok := s.tokenIndex(address)
		if !ok {
			continue
		}

		table := s.relax(src, amount, maxHops, closedLoops)

		for k := 2; k <= maxHops; k++ {
			if !table.seen[k][src] {
				continue
			}
			out := table.amount[k][src]
			if out.Cmp(amount) <= 0 {
				continue
			}

			hops := s.reconstruct(table, src, k)
			c := buildCycle(hops, amount, out)

			key := cycleKey(c.Pools)
			if seen[key] {
				continue
			}
			seen[key] = true
			found = append(found, c)
		}
	}

	if found == nil {
		return []Cycle{}
	}

	sort.Slice(found, func(i, j int) bool {
		if found[i].ProfitBps != found[j].ProfitBps {
			return found[i].ProfitBps > found[j].ProfitBps
		}
		return cycleKey(found[i].Pools) < cycleKey(found[j].Pools)
	})
	return found
}

func buildCycle(hops []Hop, amountIn, amountOut *big.Int) Cycle {
	c := Cycle{
		Tokens:    make([]string, 0, len(hops)+1),
		Symbols:   make([]string, 0, len(hops)+1),
		Pools:     make([]string, 0, len(hops)),
		AmountIn:  new(big.Int).Set(amountIn),
		AmountOut: new(big.Int).Set(amountOut),
	}

	c.Tokens = append(c.Tokens, hops[0].TokenIn)
	c.Symbols = append(c.Symbols, hops[0].TokenInSymbol)
	for _, h := range hops {
		c.Tokens = append(c.Tokens, h.TokenOut)
		c.Symbols = append(c.Symbols, h.TokenOutSymbol)
		c.Pools = append(c.Pools, h.PoolAddress)
	}

	gain := new(big.Int).Sub(amountOut, amountIn)
	gain.Mul(gain, big.NewInt(bpsScale))
	gain.Div(gain, amountIn)
	if gain.IsInt64() {
		c.ProfitBps = gain.Int64()
	} else {
		c.ProfitBps = int64(^uint64(0) >> 1)
	}

	return c
}

// cycleKey identifies a loop by its pool set, so that the same loop entered at
// a different token collapses to one entry.
func cycleKey(pools []string) string {
	norm := make([]string, len(pools))
	for i, p := range pools {
		norm[i] = strings.ToLower(p)
	}
	sort.Strings(norm)
	return strings.Join(norm, "|")
}
