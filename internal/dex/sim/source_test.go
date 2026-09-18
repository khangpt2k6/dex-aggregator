package sim

import (
	"context"
	"math/big"
	"strings"
	"testing"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

func load(t *testing.T, seed int64) []dex.Pool {
	t.Helper()
	pools, err := New(seed).Pools(context.Background())
	if err != nil {
		t.Fatalf("Pools(): %v", err)
	}
	if len(pools) == 0 {
		t.Fatal("Pools() returned nothing")
	}
	return pools
}

// The point of the simulator is that the repository behaves the same on every
// machine. If the same seed produced different numbers, a quote in the README
// would not match what a reader saw.
func TestSameSeedIsReproducible(t *testing.T) {
	a := load(t, 42)
	b := load(t, 42)

	if len(a) != len(b) {
		t.Fatalf("pool counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Address != b[i].Address {
			t.Fatalf("pool %d address differs: %s vs %s", i, a[i].Address, b[i].Address)
		}
		if fingerprint(a[i]) != fingerprint(b[i]) {
			t.Errorf("pool %s state differs between runs:\n %s\n %s", a[i].Address, fingerprint(a[i]), fingerprint(b[i]))
		}
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	a := load(t, 1)
	b := load(t, 2)

	same := 0
	for i := range a {
		if i < len(b) && fingerprint(a[i]) == fingerprint(b[i]) {
			same++
		}
	}
	if same == len(a) {
		t.Error("two different seeds produced identical state")
	}
}

func fingerprint(p dex.Pool) string {
	var sb strings.Builder
	sb.WriteString(p.Address)
	sb.WriteString("|")
	sb.WriteString(string(p.Protocol))
	sb.WriteString("|")
	for _, n := range []*big.Int{p.Reserve0, p.Reserve1, p.SqrtPriceX96, p.Liquidity} {
		if n != nil {
			sb.WriteString(n.String())
		}
		sb.WriteString(",")
	}
	for _, tk := range p.Ticks {
		sb.WriteString(tk.LiquidityNet.String())
		sb.WriteString(";")
	}
	return sb.String()
}

// Simulated state must never be mistaken for live state, in the API or the UI.
func TestEveryPoolIsMarkedSimulated(t *testing.T) {
	for _, p := range load(t, 7) {
		if !p.Simulated {
			t.Errorf("pool %s is not marked Simulated", p.Address)
		}
	}
}

func TestBothProtocolsPresent(t *testing.T) {
	counts := map[dex.Protocol]int{}
	for _, p := range load(t, 7) {
		counts[p.Protocol]++
	}

	if counts[dex.UniswapV3] == 0 {
		t.Error("no Uniswap V3 pools generated")
	}
	if counts[dex.SushiswapV2] == 0 {
		t.Error("no Sushiswap V2 pools generated")
	}
}

// A disconnected token would make some quote requests fail for reasons that
// have nothing to do with the router.
func TestGraphIsConnected(t *testing.T) {
	pools := load(t, 7)

	adj := map[string][]string{}
	for _, p := range pools {
		a := strings.ToLower(p.Token0.Address)
		b := strings.ToLower(p.Token1.Address)
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}

	weth, ok := dex.TokenBySymbol("WETH")
	if !ok {
		t.Fatal("WETH not in registry")
	}

	seen := map[string]bool{}
	queue := []string{strings.ToLower(weth.Address)}
	seen[queue[0]] = true

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range adj[cur] {
			if !seen[next] {
				seen[next] = true
				queue = append(queue, next)
			}
		}
	}

	for _, tok := range dex.Tokens() {
		if !seen[strings.ToLower(tok.Address)] {
			t.Errorf("%s is unreachable from WETH", tok.Symbol)
		}
	}
}

func TestV3PoolsHaveTickWindow(t *testing.T) {
	for _, p := range load(t, 7) {
		if p.Protocol != dex.UniswapV3 {
			continue
		}
		if len(p.Ticks) < 4 {
			t.Errorf("V3 pool %s has %d ticks, want at least 4", p.Address, len(p.Ticks))
		}
		if p.TickSpacing <= 0 {
			t.Errorf("V3 pool %s has tick spacing %d, want positive", p.Address, p.TickSpacing)
		}

		below, above := 0, 0
		for _, tk := range p.Ticks {
			if tk.Index < p.Tick {
				below++
			}
			if tk.Index > p.Tick {
				above++
			}
		}
		if below == 0 || above == 0 {
			t.Errorf("V3 pool %s has %d ticks below and %d above the current tick, want both sides covered", p.Address, below, above)
		}
	}
}

// Every generated pool must actually be quotable. A pool that errors on a
// normal-sized swap is dead weight in the graph.
func TestGeneratedPoolsAreQuotable(t *testing.T) {
	for _, p := range load(t, 7) {
		pool := p
		one := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(pool.Token0.Decimals)), nil)
		probe := new(big.Int).Div(one, big.NewInt(100))
		if probe.Sign() == 0 {
			probe = big.NewInt(1)
		}

		out, err := pool.AmountOut(probe, pool.Token0.Address)
		if err != nil {
			t.Errorf("pool %s (%s %s/%s) cannot quote: %v", pool.Address, pool.Protocol, pool.Token0.Symbol, pool.Token1.Symbol, err)
			continue
		}
		if out.Sign() <= 0 {
			t.Errorf("pool %s quoted %s, want positive", pool.Address, out)
		}
	}
}

// Prices are seeded from plausible USD values, so a WETH/USDC quote should land
// in a believable range rather than being arbitrary noise.
func TestPricesAreRoughlyRealistic(t *testing.T) {
	pools := load(t, 7)

	weth, _ := dex.TokenBySymbol("WETH")
	usdc, _ := dex.TokenBySymbol("USDC")

	oneWeth := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

	found := false
	for _, p := range pools {
		if !p.Has(weth.Address) || !p.Has(usdc.Address) {
			continue
		}
		out, err := p.AmountOut(oneWeth, weth.Address)
		if err != nil {
			continue
		}
		found = true

		// Expect somewhere in the low thousands of USDC (6 decimals).
		lo := big.NewInt(1_000_000_000)  // 1000 USDC
		hi := big.NewInt(10_000_000_000) // 10000 USDC
		if out.Cmp(lo) < 0 || out.Cmp(hi) > 0 {
			t.Errorf("pool %s quotes 1 WETH = %s raw USDC, want between 1000 and 10000", p.Address, out)
		}
	}

	if !found {
		t.Error("no WETH/USDC pool was generated")
	}
}

func TestSourceName(t *testing.T) {
	if got := New(1).Name(); got == "" {
		t.Error("Name() is empty")
	}
}

func TestSourceSatisfiesPoolSourceInterface(t *testing.T) {
	var _ dex.PoolSource = New(1)
}
