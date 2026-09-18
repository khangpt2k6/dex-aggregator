package api

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/cache"
	"github.com/khangpt2k6/dex-aggregator/internal/config"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/dex/sim"
	"github.com/khangpt2k6/dex-aggregator/internal/indexer"
)

func newTestServer(t *testing.T) http.Handler {
	t.Helper()

	cfg := &config.Config{MaxHops: 3, HTTPAddr: ":0"}
	h := cache.NewHolder()
	ix := indexer.New([]dex.PoolSource{sim.New(1337)}, h, cache.NewNoopStore(), time.Minute)

	if err := ix.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("seeding the snapshot: %v", err)
	}

	return New(h, ix, cfg).Handler()
}

// emptyServer has no snapshot, which is what a process looks like in the
// moment between starting and its first refresh completing.
func emptyServer(t *testing.T) http.Handler {
	t.Helper()

	cfg := &config.Config{MaxHops: 3}
	h := cache.NewHolder()
	ix := indexer.New([]dex.PoolSource{sim.New(1)}, h, cache.NewNoopStore(), time.Minute)
	return New(h, ix, cfg).Handler()
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	return out
}

func TestQuoteReturnsAChainedRoute(t *testing.T) {
	h := newTestServer(t)

	rec := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	q := decode[QuoteResponse](t, rec)

	if len(q.Route) == 0 {
		t.Fatal("route is empty")
	}
	if q.Hops != len(q.Route) {
		t.Errorf("Hops = %d, want %d", q.Hops, len(q.Route))
	}

	// Each hop must hand its output to the next hop's input, or the route
	// reported is not the route that was priced.
	for i := 1; i < len(q.Route); i++ {
		if q.Route[i].AmountIn != q.Route[i-1].AmountOut {
			t.Errorf("hop %d amountIn %s != hop %d amountOut %s",
				i, q.Route[i].AmountIn, i-1, q.Route[i-1].AmountOut)
		}
		if !strings.EqualFold(q.Route[i].TokenIn, q.Route[i-1].TokenOut) {
			t.Errorf("hop %d tokenIn %s != hop %d tokenOut %s",
				i, q.Route[i].TokenIn, i-1, q.Route[i-1].TokenOut)
		}
	}

	if q.Route[0].AmountIn != q.AmountIn {
		t.Errorf("first hop amountIn %s != quote amountIn %s", q.Route[0].AmountIn, q.AmountIn)
	}
	if q.Route[len(q.Route)-1].AmountOut != q.AmountOut {
		t.Errorf("last hop amountOut %s != quote amountOut %s", q.Route[len(q.Route)-1].AmountOut, q.AmountOut)
	}
	if !q.Simulated {
		t.Error("Simulated = false for a quote built from simulated pools")
	}
	if q.SnapshotAgeMs < 0 {
		t.Errorf("SnapshotAgeMs = %d, want non-negative", q.SnapshotAgeMs)
	}
}

// Amounts must be JSON strings. A JSON number is a float64 in every browser,
// and 18-decimal amounts are far past the point where that is exact.
func TestQuoteAmountsAreStringsNotNumbers(t *testing.T) {
	h := newTestServer(t)

	rec := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, field := range []string{"amountIn", "amountOut", "minReceived"} {
		v, ok := raw[field]
		if !ok {
			t.Errorf("%s missing from response", field)
			continue
		}
		if !strings.HasPrefix(string(v), `"`) {
			t.Errorf("%s = %s, want a JSON string to preserve precision", field, v)
		}
	}
}

func TestQuoteMinReceivedAppliesSlippage(t *testing.T) {
	h := newTestServer(t)

	rec := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0&slippageBps=100")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	q := decode[QuoteResponse](t, rec)

	out, ok := new(big.Int).SetString(q.AmountOut, 10)
	if !ok {
		t.Fatalf("amountOut %q is not an integer", q.AmountOut)
	}
	minRecv, ok := new(big.Int).SetString(q.MinReceived, 10)
	if !ok {
		t.Fatalf("minReceived %q is not an integer", q.MinReceived)
	}

	want := new(big.Int).Div(new(big.Int).Mul(out, big.NewInt(9900)), big.NewInt(10000))
	if minRecv.Cmp(want) != 0 {
		t.Errorf("minReceived = %s, want %s (amountOut %s less 100 bps)", minRecv, want, out)
	}
	if q.SlippageBps != 100 {
		t.Errorf("SlippageBps = %d, want 100", q.SlippageBps)
	}
}

func TestQuoteAcceptsRawBaseUnits(t *testing.T) {
	h := newTestServer(t)

	whole := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0")
	raw := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1000000000000000000")

	if whole.Code != http.StatusOK || raw.Code != http.StatusOK {
		t.Fatalf("statuses = %d and %d", whole.Code, raw.Code)
	}

	a := decode[QuoteResponse](t, whole)
	b := decode[QuoteResponse](t, raw)

	if a.AmountOut != b.AmountOut {
		t.Errorf("1.0 WETH quoted %s but 1e18 base units quoted %s; they are the same amount", a.AmountOut, b.AmountOut)
	}
}

func TestQuoteAcceptsAddressesAsWellAsSymbols(t *testing.T) {
	h := newTestServer(t)

	weth, _ := dex.TokenBySymbol("WETH")
	usdc, _ := dex.TokenBySymbol("USDC")

	bySymbol := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0")
	byAddress := get(t, h, "/api/v1/quote?tokenIn="+strings.ToLower(weth.Address)+"&tokenOut="+usdc.Address+"&amountIn=1.0")

	if byAddress.Code != http.StatusOK {
		t.Fatalf("address form status = %d, body = %s", byAddress.Code, byAddress.Body.String())
	}
	if decode[QuoteResponse](t, bySymbol).AmountOut != decode[QuoteResponse](t, byAddress).AmountOut {
		t.Error("symbol and address forms returned different quotes")
	}
}

func TestQuoteBadRequests(t *testing.T) {
	h := newTestServer(t)

	cases := []struct {
		name string
		path string
		want int
	}{
		{"missing tokenIn", "/api/v1/quote?tokenOut=USDC&amountIn=1.0", http.StatusBadRequest},
		{"missing tokenOut", "/api/v1/quote?tokenIn=WETH&amountIn=1.0", http.StatusBadRequest},
		{"missing amountIn", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC", http.StatusBadRequest},
		{"unknown token", "/api/v1/quote?tokenIn=NOTATOKEN&tokenOut=USDC&amountIn=1.0", http.StatusBadRequest},
		{"zero amount", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=0", http.StatusBadRequest},
		{"negative amount", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=-5", http.StatusBadRequest},
		{"non-numeric amount", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=banana", http.StatusBadRequest},
		{"maxHops too low", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0&maxHops=0", http.StatusBadRequest},
		{"maxHops too high", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0&maxHops=99", http.StatusBadRequest},
		{"slippage out of range", "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0&slippageBps=20000", http.StatusBadRequest},
		{"same token both sides", "/api/v1/quote?tokenIn=WETH&tokenOut=WETH&amountIn=1.0", http.StatusBadRequest},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := get(t, h, c.path)
			if rec.Code != c.want {
				t.Errorf("status = %d, want %d (body %s)", rec.Code, c.want, rec.Body.String())
			}

			e := decode[ErrorResponse](t, rec)
			if e.Error == "" {
				t.Error("error response has no message")
			}
		})
	}
}

func TestQuoteBeforeFirstSnapshot(t *testing.T) {
	rec := get(t, emptyServer(t), "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 while warming up", rec.Code)
	}
}

func TestTokensEndpoint(t *testing.T) {
	rec := get(t, newTestServer(t), "/api/v1/tokens")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	resp := decode[TokensResponse](t, rec)
	if resp.Count == 0 || len(resp.Tokens) != resp.Count {
		t.Errorf("Count = %d, len(Tokens) = %d", resp.Count, len(resp.Tokens))
	}
	for _, tok := range resp.Tokens {
		if tok.Symbol == "" || tok.Address == "" {
			t.Errorf("token %+v is incomplete", tok)
		}
	}
}

func TestPoolsEndpoint(t *testing.T) {
	h := newTestServer(t)

	rec := get(t, h, "/api/v1/pools")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	resp := decode[PoolsResponse](t, rec)
	if resp.Count == 0 {
		t.Fatal("no pools reported")
	}
	for _, p := range resp.Pools {
		if !p.Simulated {
			t.Errorf("pool %s is not flagged simulated", p.Address)
		}
	}

	limited := decode[PoolsResponse](t, get(t, h, "/api/v1/pools?limit=3"))
	if len(limited.Pools) != 3 {
		t.Errorf("limit=3 returned %d pools", len(limited.Pools))
	}
	if limited.Count != resp.Count {
		t.Errorf("limit changed the reported total: %d vs %d", limited.Count, resp.Count)
	}
}

func TestArbitrageEndpoint(t *testing.T) {
	rec := get(t, newTestServer(t), "/api/v1/arbitrage")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	resp := decode[ArbitrageResponse](t, rec)
	if resp.Cycles == nil {
		t.Error("cycles is null, want an empty array so clients can iterate it")
	}
	if resp.Note == "" {
		t.Error("arbitrage response has no caveat; a bare profit figure invites someone to trade on it")
	}
	for _, c := range resp.Cycles {
		if c.ProfitBps <= 0 {
			t.Errorf("reported cycle with %d bps profit, want positive", c.ProfitBps)
		}
	}
}

func TestHealthOK(t *testing.T) {
	rec := get(t, newTestServer(t), "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	resp := decode[HealthResponse](t, rec)
	if resp.Status != "ok" {
		t.Errorf("Status = %q, want ok", resp.Status)
	}
	if resp.Pools == 0 {
		t.Error("Pools = 0")
	}
	if resp.Mode != "simulated" {
		t.Errorf("Mode = %q, want simulated", resp.Mode)
	}
}

func TestHealthUnavailableBeforeFirstSnapshot(t *testing.T) {
	rec := get(t, emptyServer(t), "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 before the first snapshot", rec.Code)
	}
	if got := decode[HealthResponse](t, rec).Status; got != "warming" {
		t.Errorf("Status = %q, want warming", got)
	}
}

// A snapshot older than several refresh intervals means the indexer is not
// keeping up, and a load balancer should stop sending traffic here.
func TestHealthReportsStaleSnapshot(t *testing.T) {
	cfg := &config.Config{MaxHops: 3}
	h := cache.NewHolder()

	// A one-nanosecond interval makes any snapshot instantly stale.
	ix := indexer.New([]dex.PoolSource{sim.New(1)}, h, cache.NewNoopStore(), time.Nanosecond)
	if err := ix.RefreshOnce(context.Background()); err != nil {
		t.Fatalf("RefreshOnce: %v", err)
	}
	time.Sleep(time.Millisecond)

	rec := get(t, New(h, ix, cfg).Handler(), "/healthz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for a stale snapshot", rec.Code)
	}
	if got := decode[HealthResponse](t, rec).Status; got != "stale" {
		t.Errorf("Status = %q, want stale", got)
	}
}

func TestStatusEndpoint(t *testing.T) {
	rec := get(t, newTestServer(t), "/api/v1/status")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	resp := decode[StatusResponse](t, rec)
	if resp.Mode != "simulated" {
		t.Errorf("Mode = %q, want simulated", resp.Mode)
	}
	if resp.Indexer.Refreshes == 0 {
		t.Error("Indexer.Refreshes = 0")
	}
	if resp.MaxHops != 3 {
		t.Errorf("MaxHops = %d, want 3", resp.MaxHops)
	}
}

func TestMetricsEndpointExposesRouterHistogram(t *testing.T) {
	h := newTestServer(t)

	// Generate a quote so the histogram has an observation.
	if rec := get(t, h, "/api/v1/quote?tokenIn=WETH&tokenOut=USDC&amountIn=1.0"); rec.Code != http.StatusOK {
		t.Fatalf("seeding quote: %d", rec.Code)
	}

	rec := get(t, h, "/metrics")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	body := rec.Body.String()
	for _, want := range []string{"dexagg_router_duration_seconds", "dexagg_http_requests_total"} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics output is missing %s", want)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	rec := httptest.NewRecorder()
	newTestServer(t).ServeHTTP(rec, httptest.NewRequest(http.MethodOptions, "/api/v1/quote", nil))

	if rec.Code != http.StatusNoContent {
		t.Errorf("preflight status = %d, want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
}

// The router budget is about search time, so the reported figure must exclude
// HTTP overhead and be plausibly small.
func TestQuoteReportsRouterTime(t *testing.T) {
	rec := get(t, newTestServer(t), "/api/v1/quote?tokenIn=WETH&tokenOut=WBTC&amountIn=1.0")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	q := decode[QuoteResponse](t, rec)
	if q.RouterMicros < 0 {
		t.Errorf("RouterMicros = %d, want non-negative", q.RouterMicros)
	}
	if q.RouterMicros > 100_000 {
		t.Errorf("RouterMicros = %d (over 100ms), far outside the budget", q.RouterMicros)
	}
}
