# dex-aggregator

A non-custodial DEX aggregator. It indexes liquidity across Uniswap V3 and
Sushiswap V2, then finds the swap route that maximises output for a given input
amount, accounting for real AMM price impact rather than quoted spot price.

Router p99 is **2.3 ms** over a warm in-memory graph, enforced by a test that
fails the build if it regresses past 10 ms.

```
go run ./cmd/aggregator          # no config, no API key, runs immediately
curl 'localhost:8080/api/v1/quote?tokenIn=WETH&tokenOut=WBTC&amountIn=1.0'
```

Or the whole stack including the UI:

```
docker compose up --build        # then open http://localhost:3000
```

---

## Why a shortest-path router is the wrong tool here, and what to use instead

The obvious way to build this is to treat each pool as a graph edge weighted by
its quoted price and run Dijkstra. That is wrong, and it is wrong in the case
that matters most.

Spot price is the marginal price at zero trade size. Actual execution price is a
non-linear function of size, and the shape of that function is different for
every pool. A pool with the best quoted price and shallow liquidity is the
*worst* venue for a large trade. So a router built on static spot-price edge
weights returns the wrong pool exactly when the trade is big enough for the
answer to matter.

The correct edge weight is

```
w(edge, amountIn) = -log( amountOut(edge, amountIn) / amountIn )
```

evaluated at the amount actually arriving at that edge. Maximising final output
is then the same as minimising the sum of weights, because log turns the product
of per-hop exchange rates into a sum.

Two things follow from that, and they determine the whole design:

1. **Weights are not known until the amount is known.** The search has to carry
   real token amounts forward rather than carry prices. That rules out Dijkstra,
   whose correctness argument depends on static, non-negative weights.
2. **Weights go negative** whenever a hop gains value. Negative edges are normal
   here, not exceptional.

So the router is a **Bellman-Ford relaxation indexed by hop count**. Round `k`
computes, for every token, the largest amount reachable using exactly `k` hops.

Keeping only the best amount per `(hops, token)` is valid because every edge
function is monotonically increasing: if one path delivers more of a token than
another, it also delivers more after any continuation. That is the
optimal-substructure property Bellman-Ford needs, and it survives the
non-linearity that breaks the spot-price formulation.

The test that pins this down is
[`TestFindBestRouteDependsOnTradeSize`](internal/graph/router_test.go). Two pools
on the same pair: one shallow with a better quoted price, one deep with a worse
one. The router must pick the shallow pool for a small trade and the deep pool
for a large one. A spot-price router picks the shallow pool both times and fails
that test.

### Arbitrage detection falls out for free

A negative-weight cycle in `-log` space is a closed loop that returns more of a
token than it consumed, which is an arbitrage opportunity. Bellman-Ford detects
those as an ordinary consequence of how it works, so
[`/api/v1/arbitrage`](internal/graph/arbitrage.go) costs no additional
algorithmic work:

```
GET /api/v1/arbitrage
  DAI -> WETH -> WBTC -> DAI    22 bps
  DAI -> WBTC -> DAI             3 bps
```

This is gross of gas and MEV competition, against a snapshot that is already
stale by the time you read it. It is not executable profit and the endpoint says
so in its own response.

---

## Architecture

Two paths, deliberately separated. That separation is the reason the latency
target is achievable rather than aspirational.

```
                    ┌──────────────── background, async, tolerant of failure
                    │
  Ethereum RPC ─▶ worker pool ─▶ Multicall3 ─▶ indexer ─▶ BuildSnapshot
   (or simulator)   bounded       batching       loop         │
                    rate limited                              │ atomic.Pointer
                    breakered                                 ▼
                                                        ┌──────────┐
                                                        │ Snapshot │ immutable
                                                        └──────────┘
                                                              │ lock-free load
  HTTP GET /quote ────────────────────────────────────────────┤
                    no network, no Redis, no mutex            ▼
                                                     Bellman-Ford relaxation
```

**Background path.** An indexer polls each pool source on a ticker, fans the
contract reads through a bounded worker pool, batches them into Multicall3
calls, builds a fresh immutable graph, and publishes it with a single atomic
pointer store. It writes the same state to Redis so siblings and restarts can
warm without hammering the provider.

**Hot path.** A quote request loads the snapshot pointer and searches it. No
network call, no Redis round trip, no lock, no mutation. Redis is explicitly
*not* a request-time cache; it is shared state and warm-start storage.

If a refresh fails, the previous snapshot keeps serving. Stale quotes degrade
quality; they do not take the service down. If one source fails and another
succeeds, the healthy one is still indexed.

### Package layout

```
cmd/aggregator/      process entry, wiring, graceful shutdown
internal/amm/        big.Int swap math: V2 constant product, V3 tick crossing
internal/dex/        Pool model, PoolSource interface, token registry
  └ sim/             deterministic simulated source
  └ onchain/         live Uniswap V3 + Sushiswap V2 over Multicall3
internal/rpc/        worker pool, rate limiter, circuit breaker, ABI codec
internal/graph/      immutable snapshot, amount-aware router, arbitrage
internal/cache/      atomic snapshot holder, Redis store
internal/indexer/    refresh loop
internal/api/        HTTP handlers, websocket price stream, metrics
web/                 React + TypeScript + Vite frontend
```

Dependencies run strictly downward. `amm` imports nothing internal. `graph`
knows nothing about HTTP. `api` knows nothing about which DEX a pool came from.

---

## Hitting the latency budget

The first implementation missed it. p99 was **12.5 ms** against a 10 ms budget,
while p50 was a comfortable 2.1 ms. A long tail with a healthy median usually
means garbage collection, not algorithmic cost, and profiling confirmed it:

```
mallocgc        20.8%
makeslice       16.7%
big.nat.make    74.9% of all allocations
```

Running the same measurement with `GOGC=400` dropped p99 from 11.6 ms to 3.1 ms
with byte-identical results, which settled it. 87% of allocations came from the
V3 tick-crossing loop, where every `big.Int` operation allocated a fresh backing
array, and a single query performed over a thousand of them.

Three changes:

1. **`amm.Scratch`**, a reusable `big.Int` workspace threaded through the swap
   math. Receivers keep their backing arrays across calls instead of allocating
   new ones. The router creates one per query and only copies results that
   actually win, so losing candidates cost nothing.
2. **Tick pre-sorting and sqrt-ratio precomputation** moved to snapshot build
   time. `GetSqrtRatioAtTick` is twenty `big.Int` multiplications, and the
   router was calling it for every tick of every candidate pool on every
   relaxation round, for state that only changes once per refresh.
3. **Integer token indices** in the adjacency structure instead of comparing
   42-character address strings in the inner loop.

| maxHops=3, 40 tokens, 300 pools | before | after |
|---|---|---|
| allocations per query | 18,490 | 2,147 |
| garbage per query | 901 KB | 105 KB |
| **router p99** | **12.5 ms** | **2.3 ms** |
| worst observed | 17.7 ms | 3.8 ms |

Current measurement, on an AMD Ryzen AI 7 350:

```
router latency over 1951 queries: p50=1.00ms p95=1.98ms p99=2.32ms max=3.84ms
```

### What that number does and does not mean

It is **router time over a warm in-memory snapshot**. It excludes RPC latency,
network time, and JSON serialisation. That is the honest scope of the claim, and
keeping the snapshot warm is precisely what the background indexer exists to do.

It is enforced, not asserted.
[`TestRouterLatencyBudget`](internal/graph/latency_test.go) builds a 300-pool
graph, runs 2000 queries, and fails the build if p99 exceeds 10 ms. It also
fails if fewer than 80% of queries actually returned a route, so the budget
cannot be met by failing fast. It runs in CI on every push.

---

## Not getting rate-limited

Public Ethereum nodes throttle aggressively, and a refresh that issues one
`eth_call` per pool is throttled within seconds. Four layers in
[`internal/rpc`](internal/rpc/):

- **Bounded worker pool.** A semaphore caps in-flight calls at a configured
  number. Each caller keeps its own context, so a cancelled request stops
  consuming quota immediately rather than waiting to be dequeued.
- **Multicall3 batching.** One `eth_call` performs up to 100 contract reads.
  Indexing the full pool set costs a handful of requests instead of hundreds.
- **Token bucket.** Sustained rate stays under quota while still allowing a
  burst for a refresh cycle.
- **Circuit breaker.** After consecutive failures it rejects immediately instead
  of piling timeouts onto a provider that is already struggling. Half-open
  admits exactly one probe, so the cooldown does not release a thundering herd.

Retries use exponential backoff with **full jitter**. Without jitter, a batch
that fails together retries together, which recreates the burst that caused the
failure.

Pool addresses are **derived locally with CREATE2** rather than discovered by
querying the factory, so discovery costs zero RPC calls. The derivation is
[checked against four real mainnet pool addresses](internal/dex/onchain/onchain_test.go)
because a wrong salt layout produces a perfectly well-formed address that simply
holds nothing, and every probe would come back reverted with no indication why.

---

## Running it

### No configuration

```bash
go run ./cmd/aggregator
```

With no `ETH_RPC_URL`, pool state comes from a deterministic simulator: real
mainnet token addresses, prices anchored to plausible USD values, liquidity
concentrated the way a real V3 position is, and the same hub-and-spoke pair
topology mainnet actually has. Identical output on every machine for a given
seed.

Every simulated pool is flagged, and that flag travels through the API into the
UI. Simulated data is never presented as live.

### Against real mainnet

```bash
ETH_RPC_URL=https://eth-mainnet.g.alchemy.com/v2/YOUR_KEY go run ./cmd/aggregator
```

### Configuration

| Variable | Default | Meaning |
|---|---|---|
| `HTTP_ADDR` | `:8080` | Listen address |
| `ETH_RPC_URL` | *(empty)* | JSON-RPC endpoint. Empty selects the simulator |
| `REDIS_URL` | *(empty)* | Shared snapshot store. Empty runs in-memory only |
| `RPC_WORKERS` | `16` | Max concurrent RPC calls |
| `RPC_RATE_PER_SEC` | `10` | Sustained request rate |
| `REFRESH_INTERVAL` | `6s` | How often the graph is rebuilt |
| `MAX_HOPS` | `3` | Route length cap, 1 to 6 |
| `SIM_SEED` | `1337` | Makes simulated state reproducible |

Redis being unreachable is not fatal. It costs a slower cold start and no
cross-instance sharing, and the service logs it and continues.

---

## API

```
GET  /api/v1/quote      tokenIn, tokenOut, amountIn, maxHops, slippageBps
GET  /api/v1/tokens     supported tokens
GET  /api/v1/pools      indexed pools
GET  /api/v1/arbitrage  detected negative cycles
GET  /api/v1/status     indexer health
GET  /healthz           liveness plus snapshot age
GET  /metrics           prometheus
WS   /ws/prices         streaming prices
```

`tokenIn` and `tokenOut` accept a symbol or a contract address. `amountIn`
accepts whole units (`1.5`) or raw base units (`1500000000000000000`).

```json
{
  "tokenInSymbol": "WETH", "tokenOutSymbol": "USDC",
  "amountIn": "1000000000000000000",
  "amountOut": "3000849452",
  "minReceived": "2985845209",
  "priceImpactBps": 48,
  "route": [
    { "poolAddress": "0x...", "protocol": "uniswap-v3", "feeBps": 5,
      "tokenInSymbol": "WETH", "tokenOutSymbol": "WBTC",
      "amountIn": "1000000000000000000", "amountOut": "5020412" }
  ],
  "hops": 3,
  "snapshotAgeMs": 1430,
  "routerMicros": 801,
  "simulated": true
}
```

**Every amount is a decimal string, never a JSON number.** A JSON number is a
`float64` in every browser, and an 18-decimal amount is far past the 2^53 where
that stops being exact. Sending a quote as a number would corrupt it on arrival.
The frontend does all formatting with `BigInt` for the same reason.

---

## Correctness notes

Things that were wrong at some point and are now pinned by tests.

**A swap route must be a simple path.** The router originally returned
`WETH -> WBTC -> WETH -> WBTC`, using the same pool twice, because the simulated
graph contained a profitable loop and folding it in genuinely produced a larger
number. That quote is not executable: every hop is priced against one snapshot,
so the second pass through a pool would meet the price the first pass just
moved. Swap routes are now simple paths; loops are reported separately on
`/api/v1/arbitrage`. Pinned by
[`TestRouteNeverReusesAPoolOrRevisitsAToken`](internal/graph/router_test.go).

**Solidity sign-extends narrow signed types.** An `int24` tick of `-1` arrives as
thirty-two bytes of `0xff`. Decoding the raw word gives `2^256-1` instead of
`-1`, so the decoder masks to the declared width before applying the sign check.

**Ethereum uses original Keccak padding, not NIST SHA-3.** They produce different
digests. Using `sha3.New256` would compute plausible-looking function selectors
that no contract on earth responds to, so every selector is computed from its
signature and checked against a known value.

**The V3 stepper is validated against an independent implementation.** Inside a
single tick range, a V3 pool is algebraically a constant product pool over
virtual reserves. `TestSwapV3MatchesConstantProductWithinOneTick` checks the
tick-crossing math against the separately written V2 implementation and requires
agreement to one part per million.

---

## Limitations

Stated plainly, because a project that hides these is harder to trust than one
that names them.

- **The V3 tick window is bounded.** Reading a pool's entire tick map would cost
  hundreds of RPC calls per pool. A swap large enough to run past the fetched
  window returns "insufficient liquidity" rather than extrapolating, and the
  router skips that pool. Correct trades near the current price; conservative
  for very large ones.
- **No route splitting.** Real aggregators split one trade across several pools
  in parallel. This one picks a single best path. Splitting is a convex
  optimisation problem on top of the routing problem, not an extension of it.
- **No gas cost in the objective.** The router maximises output, not output
  minus gas. A three-hop route that beats a one-hop route by 2 bps may lose
  after gas. `MAX_HOPS` is the blunt instrument standing in for this.
- **Best-amount-per-state is a heuristic once revisits are forbidden.** Keeping
  only the best amount per `(hops, token)` can discard a path whose continuation
  would have been better under the simple-path rule. Enumerating simple paths
  outright is exponential in hop count. At realistic hop limits the loss is
  negligible.
- **Read-only.** No wallet, no signing, no transaction submission, no MEV
  protection. It returns a route; it does not execute one.

---

## Tests

```bash
make test        # everything
make race        # under the race detector
make latency     # the p99 budget gate
make bench       # router benchmarks
```

CI runs `go vet`, the full suite under `-race`, the latency gate, and the
frontend typecheck and build on every push.

---

## Stack

Go 1.26 standard library, plus `redis/go-redis`, `prometheus/client_golang`,
`gorilla/websocket` and `golang.org/x/crypto` for Keccak. No web framework, no
ORM, no go-ethereum dependency: the ABI codec and JSON-RPC transport are about
three hundred lines and avoid pulling in a client library an order of magnitude
larger than this project.

Frontend is React 19 + TypeScript + Vite with `lightweight-charts`, and plain
CSS.
