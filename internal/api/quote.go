package api

import (
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/graph"
)

// HopJSON is one swap in a route, as returned to the client.
//
// Amounts are decimal strings. JSON numbers are float64 in every browser, and
// a pool reserve at 18 decimals is far past the 2^53 where float64 stops being
// exact. Sending a quote as a number would corrupt it on arrival.
type HopJSON struct {
	PoolAddress    string       `json:"poolAddress"`
	Protocol       dex.Protocol `json:"protocol"`
	FeeBps         uint32       `json:"feeBps"`
	TokenIn        string       `json:"tokenIn"`
	TokenOut       string       `json:"tokenOut"`
	TokenInSymbol  string       `json:"tokenInSymbol"`
	TokenOutSymbol string       `json:"tokenOutSymbol"`
	AmountIn       string       `json:"amountIn"`
	AmountOut      string       `json:"amountOut"`
}

// QuoteResponse is the answer to a quote request.
type QuoteResponse struct {
	TokenIn        string    `json:"tokenIn"`
	TokenOut       string    `json:"tokenOut"`
	TokenInSymbol  string    `json:"tokenInSymbol"`
	TokenOutSymbol string    `json:"tokenOutSymbol"`
	AmountIn       string    `json:"amountIn"`
	AmountOut      string    `json:"amountOut"`
	MinReceived    string    `json:"minReceived"`
	SlippageBps    int64     `json:"slippageBps"`
	PriceImpactBps int64     `json:"priceImpactBps"`
	Route          []HopJSON `json:"route"`
	Hops           int       `json:"hops"`

	// SnapshotAgeMs says how stale the pool state behind this quote is, so a
	// caller can decide for itself whether to trust the number.
	SnapshotAgeMs int64 `json:"snapshotAgeMs"`

	// RouterMicros is time spent searching, excluding HTTP overhead. This is
	// the figure the latency budget is about.
	RouterMicros int64 `json:"routerMicros"`

	// Simulated is true when the pools behind this quote came from the
	// deterministic simulator rather than a node. It is carried all the way to
	// the UI so demo data is never mistaken for live data.
	Simulated bool `json:"simulated"`
}

// defaultSlippageBps is 50 bps, the usual default in swap interfaces.
const defaultSlippageBps = 50

func (s *Server) handleQuote(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	snapshot := s.holder.Get()
	if snapshot == nil {
		writeError(w, http.StatusServiceUnavailable, "warming up", "no pool snapshot has been built yet")
		return
	}

	tokenIn, ok := resolveToken(q.Get("tokenIn"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad tokenIn", "tokenIn must be a known symbol or contract address")
		return
	}
	tokenOut, ok := resolveToken(q.Get("tokenOut"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad tokenOut", "tokenOut must be a known symbol or contract address")
		return
	}

	amountIn, err := parseAmount(q.Get("amountIn"), tokenIn)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad amountIn", err.Error())
		return
	}

	maxHops := s.cfg.MaxHops
	if raw := q.Get("maxHops"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 6 {
			writeError(w, http.StatusBadRequest, "bad maxHops", "maxHops must be an integer between 1 and 6")
			return
		}
		maxHops = n
	}

	slippageBps := int64(defaultSlippageBps)
	if raw := q.Get("slippageBps"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 || n > 10000 {
			writeError(w, http.StatusBadRequest, "bad slippageBps", "slippageBps must be an integer between 0 and 10000")
			return
		}
		slippageBps = n
	}

	start := time.Now()
	route, err := graph.FindBestRoute(snapshot, tokenIn.Address, tokenOut.Address, amountIn, maxHops)
	elapsed := time.Since(start)

	s.metrics.observeRouter(elapsed)

	if err != nil {
		switch {
		case errors.Is(err, graph.ErrNoRoute):
			writeError(w, http.StatusNotFound, "no route",
				fmt.Sprintf("no path from %s to %s within %d hops", tokenIn.Symbol, tokenOut.Symbol, maxHops))
		case errors.Is(err, graph.ErrUnknownToken):
			writeError(w, http.StatusNotFound, "token not indexed",
				"that token is known but has no indexed liquidity yet")
		default:
			writeError(w, http.StatusBadRequest, "bad request", err.Error())
		}
		return
	}

	writeJSON(w, http.StatusOK, buildQuoteResponse(snapshot, route, tokenIn, tokenOut, slippageBps, elapsed))
}

func buildQuoteResponse(
	snapshot *graph.Snapshot,
	route *graph.Route,
	tokenIn, tokenOut dex.Token,
	slippageBps int64,
	elapsed time.Duration,
) QuoteResponse {
	hops := make([]HopJSON, 0, len(route.Hops))
	simulated := false

	byAddress := map[string]bool{}
	for _, p := range snapshot.Pools() {
		if p.Simulated {
			byAddress[p.Address] = true
		}
	}

	for _, h := range route.Hops {
		if byAddress[h.PoolAddress] {
			simulated = true
		}
		hops = append(hops, HopJSON{
			PoolAddress:    h.PoolAddress,
			Protocol:       h.Protocol,
			FeeBps:         h.FeeBps,
			TokenIn:        h.TokenIn,
			TokenOut:       h.TokenOut,
			TokenInSymbol:  h.TokenInSymbol,
			TokenOutSymbol: h.TokenOutSymbol,
			AmountIn:       h.AmountIn.String(),
			AmountOut:      h.AmountOut.String(),
		})
	}

	return QuoteResponse{
		TokenIn:        tokenIn.Address,
		TokenOut:       tokenOut.Address,
		TokenInSymbol:  tokenIn.Symbol,
		TokenOutSymbol: tokenOut.Symbol,
		AmountIn:       route.AmountIn.String(),
		AmountOut:      route.AmountOut.String(),
		MinReceived:    applySlippage(route.AmountOut, slippageBps).String(),
		SlippageBps:    slippageBps,
		PriceImpactBps: route.PriceImpactBps,
		Route:          hops,
		Hops:           len(hops),
		SnapshotAgeMs:  snapshot.Age().Milliseconds(),
		RouterMicros:   elapsed.Microseconds(),
		Simulated:      simulated,
	}
}

// applySlippage returns the minimum the caller should accept, rounding down so
// the guarantee is never overstated.
func applySlippage(amount *big.Int, bps int64) *big.Int {
	out := new(big.Int).Mul(amount, big.NewInt(10000-bps))
	return out.Div(out, big.NewInt(10000))
}

// resolveToken accepts a symbol or a contract address.
func resolveToken(ref string) (dex.Token, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return dex.Token{}, false
	}
	return dex.Resolve(ref)
}

// parseAmount reads an amount in raw base units, or in whole token units when
// suffixed. A quote request that silently misreads its amount by a factor of
// 10^18 is worse than one that refuses to guess.
func parseAmount(raw string, token dex.Token) (*big.Int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("amountIn is required, in the token's base units")
	}

	// A decimal point means whole units, which is what a human types.
	if strings.Contains(raw, ".") {
		f, ok := new(big.Float).SetString(raw)
		if !ok || f.Sign() <= 0 {
			return nil, fmt.Errorf("%q is not a positive number", raw)
		}
		scale := new(big.Float).SetInt(pow10(int(token.Decimals)))
		f.Mul(f, scale)

		out, _ := f.Int(nil)
		if out.Sign() <= 0 {
			return nil, fmt.Errorf("%s %s rounds to zero at %d decimals", raw, token.Symbol, token.Decimals)
		}
		return out, nil
	}

	out, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("%q is not an integer amount in base units", raw)
	}
	if out.Sign() <= 0 {
		return nil, errors.New("amountIn must be positive")
	}
	return out, nil
}

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}
