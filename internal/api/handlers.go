package api

import (
	"math/big"
	"net/http"
	"strconv"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/graph"
	"github.com/khangpt2k6/dex-aggregator/internal/indexer"
)

// TokensResponse lists the tokens the aggregator can route.
type TokensResponse struct {
	Tokens []dex.Token `json:"tokens"`
	Count  int         `json:"count"`
}

func (s *Server) handleTokens(w http.ResponseWriter, r *http.Request) {
	tokens := dex.Tokens()
	writeJSON(w, http.StatusOK, TokensResponse{Tokens: tokens, Count: len(tokens)})
}

// PoolJSON is an indexed pool as reported to the client.
type PoolJSON struct {
	Address   string       `json:"address"`
	Protocol  dex.Protocol `json:"protocol"`
	Token0    string       `json:"token0"`
	Token1    string       `json:"token1"`
	FeeBps    uint32       `json:"feeBps"`
	Simulated bool         `json:"simulated"`

	// Reserves are only meaningful for constant product pools, and are decimal
	// strings for the same precision reason as every other amount here.
	Reserve0 string `json:"reserve0,omitempty"`
	Reserve1 string `json:"reserve1,omitempty"`

	Liquidity string `json:"liquidity,omitempty"`
	Tick      int32  `json:"tick,omitempty"`
}

// PoolsResponse lists indexed pools.
type PoolsResponse struct {
	Pools         []PoolJSON `json:"pools"`
	Count         int        `json:"count"`
	SnapshotAgeMs int64      `json:"snapshotAgeMs"`
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	snapshot := s.holder.Get()
	if snapshot == nil {
		writeError(w, http.StatusServiceUnavailable, "warming up", "no pool snapshot has been built yet")
		return
	}

	pools := snapshot.Pools()

	limit := len(pools)
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n < limit {
			limit = n
		}
	}

	out := make([]PoolJSON, 0, limit)
	for i := 0; i < limit; i++ {
		p := pools[i]
		out = append(out, PoolJSON{
			Address:   p.Address,
			Protocol:  p.Protocol,
			Token0:    p.Token0.Symbol,
			Token1:    p.Token1.Symbol,
			FeeBps:    p.FeeBps,
			Simulated: p.Simulated,
			Reserve0:  bigString(p.Reserve0),
			Reserve1:  bigString(p.Reserve1),
			Liquidity: bigString(p.Liquidity),
			Tick:      p.Tick,
		})
	}

	writeJSON(w, http.StatusOK, PoolsResponse{
		Pools:         out,
		Count:         len(pools),
		SnapshotAgeMs: snapshot.Age().Milliseconds(),
	})
}

// CycleJSON is a detected arbitrage loop.
type CycleJSON struct {
	Symbols   []string `json:"symbols"`
	Pools     []string `json:"pools"`
	AmountIn  string   `json:"amountIn"`
	AmountOut string   `json:"amountOut"`
	ProfitBps int64    `json:"profitBps"`
}

// ArbitrageResponse reports loops that return more than they consumed.
type ArbitrageResponse struct {
	Cycles []CycleJSON `json:"cycles"`
	Count  int         `json:"count"`

	// Note is returned alongside the data because an arbitrage figure with no
	// caveat invites someone to trade on it.
	Note string `json:"note"`
}

func (s *Server) handleArbitrage(w http.ResponseWriter, r *http.Request) {
	snapshot := s.holder.Get()
	if snapshot == nil {
		writeError(w, http.StatusServiceUnavailable, "warming up", "no pool snapshot has been built yet")
		return
	}

	// Probe from the liquid hubs, at a size large enough to clear fees but
	// small enough to be executable.
	probe := map[string]*big.Int{}
	for _, symbol := range []string{"WETH", "USDC", "USDT", "DAI", "WBTC"} {
		tok, ok := dex.TokenBySymbol(symbol)
		if !ok {
			continue
		}
		if _, indexed := snapshot.Token(tok.Address); !indexed {
			continue
		}
		probe[tok.Address] = probeSize(tok)
	}

	cycles := graph.FindArbitrage(snapshot, probe, s.cfg.MaxHops)

	out := make([]CycleJSON, 0, len(cycles))
	for _, c := range cycles {
		out = append(out, CycleJSON{
			Symbols:   c.Symbols,
			Pools:     c.Pools,
			AmountIn:  c.AmountIn.String(),
			AmountOut: c.AmountOut.String(),
			ProfitBps: c.ProfitBps,
		})
	}

	writeJSON(w, http.StatusOK, ArbitrageResponse{
		Cycles: out,
		Count:  len(out),
		Note:   "Gross of gas and MEV competition, and computed against a snapshot that is already stale by the time you read this. Not executable profit.",
	})
}

// probeSize returns roughly one thousand dollars' worth of a token, which is
// large enough to clear swap fees and small enough that the answer is about
// mispricing rather than about depth.
func probeSize(t dex.Token) *big.Int {
	whole := map[string]int64{
		"WETH": 1,
		"WBTC": 1,
		"USDC": 2000,
		"USDT": 2000,
		"DAI":  2000,
	}

	n, ok := whole[t.Symbol]
	if !ok {
		n = 1
	}
	return new(big.Int).Mul(big.NewInt(n), pow10(int(t.Decimals)))
}

// RPCStatus reports how the worker pool is getting on with the provider.
//
// This is the part of the service most likely to be the reason quotes have gone
// stale, so it is worth being able to see from outside: a rising Rejected count
// with an open breaker says the provider is refusing traffic, while a rising
// Retried count with a closed breaker says it is merely throttling.
type RPCStatus struct {
	InFlight     int64  `json:"inFlight"`
	Completed    int64  `json:"completed"`
	Failed       int64  `json:"failed"`
	Retried      int64  `json:"retried"`
	Rejected     int64  `json:"rejected"`
	BreakerState string `json:"breakerState"`
}

// StatusResponse describes what the service is currently serving.
type StatusResponse struct {
	Indexer        indexer.Status `json:"indexer"`
	SnapshotAgeMs  int64          `json:"snapshotAgeMs"`
	RefreshEveryMs int64          `json:"refreshEveryMs"`
	MaxHops        int            `json:"maxHops"`
	Mode           string         `json:"mode"`
	WSClients      int            `json:"wsClients"`

	// RPC is absent in simulated mode, where there is no provider to report on.
	// Reporting a row of zeroes instead would look like a healthy idle pool.
	RPC *RPCStatus `json:"rpc,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	resp := StatusResponse{
		Indexer:        s.indexer.Status(),
		SnapshotAgeMs:  s.holder.Age().Milliseconds(),
		RefreshEveryMs: s.indexer.Interval().Milliseconds(),
		MaxHops:        s.cfg.MaxHops,
		Mode:           s.mode(),
		WSClients:      s.hub.ClientCount(),
	}

	if s.rpcStats != nil {
		stats, breaker := s.rpcStats()
		resp.RPC = &RPCStatus{
			InFlight:     stats.InFlight,
			Completed:    stats.Completed,
			Failed:       stats.Failed,
			Retried:      stats.Retried,
			Rejected:     stats.Rejected,
			BreakerState: string(breaker),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) mode() string {
	if s.cfg.LiveMode() {
		return "live"
	}
	return "simulated"
}

// HealthResponse is the readiness answer.
type HealthResponse struct {
	Status        string `json:"status"`
	SnapshotAgeMs int64  `json:"snapshotAgeMs"`
	Pools         int    `json:"pools"`
	Mode          string `json:"mode"`
	Detail        string `json:"detail,omitempty"`
}

// staleAfterIntervals is how many missed refreshes make a snapshot unhealthy.
//
// One missed refresh is a hiccup and the data is still fine. Three in a row
// means the indexer is genuinely not keeping up, and a load balancer should
// stop sending traffic here.
const staleAfterIntervals = 3

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snapshot := s.holder.Get()

	if snapshot == nil {
		writeJSON(w, http.StatusServiceUnavailable, HealthResponse{
			Status: "warming",
			Mode:   s.mode(),
			Detail: "no pool snapshot has been built yet",
		})
		return
	}

	age := snapshot.Age()
	limit := s.indexer.Interval() * staleAfterIntervals

	resp := HealthResponse{
		Status:        "ok",
		SnapshotAgeMs: age.Milliseconds(),
		Pools:         len(snapshot.Pools()),
		Mode:          s.mode(),
	}

	if age > limit {
		resp.Status = "stale"
		resp.Detail = "snapshot is older than " + limit.String() + "; quotes may not reflect current liquidity"
		writeJSON(w, http.StatusServiceUnavailable, resp)
		return
	}

	if err := s.indexer.LastError(); err != nil {
		// Serving on degraded liquidity is still serving, so this stays a 200.
		// The detail is there for anyone looking.
		resp.Detail = err.Error()
	}

	writeJSON(w, http.StatusOK, resp)
}

func bigString(n *big.Int) string {
	if n == nil {
		return ""
	}
	return n.String()
}
