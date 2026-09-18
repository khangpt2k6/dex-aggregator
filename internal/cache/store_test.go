package cache

import (
	"context"
	"math/big"
	"testing"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

func v3Pool() dex.Pool {
	sqrtP, err := amm.GetSqrtRatioAtTick(-201600)
	if err != nil {
		panic(err)
	}
	liq := new(big.Int).Mul(big.NewInt(4_242_424_242), pow10(18))

	return dex.Pool{
		Address:      "0xv3pool",
		Protocol:     dex.UniswapV3,
		Token0:       weth,
		Token1:       usdc,
		FeeBps:       5,
		Simulated:    true,
		SqrtPriceX96: sqrtP,
		Liquidity:    liq,
		Tick:         -201600,
		TickSpacing:  60,
		Ticks: []amm.V3Tick{
			{Index: -202800, LiquidityNet: new(big.Int).Div(liq, big.NewInt(4))},
			{Index: -200400, LiquidityNet: new(big.Int).Neg(new(big.Int).Div(liq, big.NewInt(4)))},
		},
	}
}

// Pool amounts are big.Int, which does not survive a naive JSON round trip:
// encoding them as numbers would silently lose precision past 2^53. They must
// come back byte-identical.
func TestCodecRoundTripPreservesBigInts(t *testing.T) {
	in := append(samplePools(2), v3Pool())

	blob, err := encodePools(in)
	if err != nil {
		t.Fatalf("encodePools: %v", err)
	}

	out, err := decodePools(blob)
	if err != nil {
		t.Fatalf("decodePools: %v", err)
	}

	if len(out) != len(in) {
		t.Fatalf("decoded %d pools, want %d", len(out), len(in))
	}

	for i := range in {
		a, b := in[i], out[i]

		if a.Address != b.Address || a.Protocol != b.Protocol || a.FeeBps != b.FeeBps {
			t.Errorf("pool %d identity differs: %+v vs %+v", i, a, b)
		}
		if a.Simulated != b.Simulated {
			t.Errorf("pool %d Simulated = %v, want %v", i, b.Simulated, a.Simulated)
		}
		if a.Token0 != b.Token0 || a.Token1 != b.Token1 {
			t.Errorf("pool %d tokens differ", i)
		}

		assertBigEqual(t, i, "Reserve0", a.Reserve0, b.Reserve0)
		assertBigEqual(t, i, "Reserve1", a.Reserve1, b.Reserve1)
		assertBigEqual(t, i, "SqrtPriceX96", a.SqrtPriceX96, b.SqrtPriceX96)
		assertBigEqual(t, i, "Liquidity", a.Liquidity, b.Liquidity)

		if a.Tick != b.Tick || a.TickSpacing != b.TickSpacing {
			t.Errorf("pool %d tick state differs: %d/%d vs %d/%d", i, a.Tick, a.TickSpacing, b.Tick, b.TickSpacing)
		}
		if len(a.Ticks) != len(b.Ticks) {
			t.Fatalf("pool %d decoded %d ticks, want %d", i, len(b.Ticks), len(a.Ticks))
		}
		for j := range a.Ticks {
			if a.Ticks[j].Index != b.Ticks[j].Index {
				t.Errorf("pool %d tick %d index = %d, want %d", i, j, b.Ticks[j].Index, a.Ticks[j].Index)
			}
			assertBigEqual(t, i, "tick LiquidityNet", a.Ticks[j].LiquidityNet, b.Ticks[j].LiquidityNet)
		}
	}
}

func assertBigEqual(t *testing.T, idx int, field string, a, b *big.Int) {
	t.Helper()
	switch {
	case a == nil && b == nil:
		return
	case a == nil || b == nil:
		t.Errorf("pool %d %s nil mismatch: %v vs %v", idx, field, a, b)
	case a.Cmp(b) != 0:
		t.Errorf("pool %d %s = %s, want %s", idx, field, b, a)
	}
}

// A very large liquidity value is exactly the case a float-based encoding gets
// wrong, so check one past 2^53 explicitly.
func TestCodecHandlesValuesBeyondFloat64Precision(t *testing.T) {
	huge, _ := new(big.Int).SetString("123456789012345678901234567890123456789", 10)

	p := samplePools(1)[0]
	p.Reserve0 = huge

	blob, err := encodePools([]dex.Pool{p})
	if err != nil {
		t.Fatalf("encodePools: %v", err)
	}
	out, err := decodePools(blob)
	if err != nil {
		t.Fatalf("decodePools: %v", err)
	}

	if out[0].Reserve0.Cmp(huge) != 0 {
		t.Errorf("Reserve0 = %s, want %s", out[0].Reserve0, huge)
	}
}

func TestCodecEmpty(t *testing.T) {
	blob, err := encodePools(nil)
	if err != nil {
		t.Fatalf("encodePools(nil): %v", err)
	}
	out, err := decodePools(blob)
	if err != nil {
		t.Fatalf("decodePools: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("decoded %d pools, want 0", len(out))
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	if _, err := decodePools([]byte("not json")); err == nil {
		t.Error("decodePools on garbage returned nil error, want error")
	}
}

// Redis is optional. With no URL configured the service must still start and
// run, just without cross-instance sharing.
func TestNoopStore(t *testing.T) {
	s := NewNoopStore()
	ctx := context.Background()

	if err := s.SavePools(ctx, samplePools(3)); err != nil {
		t.Errorf("SavePools: %v", err)
	}

	got, err := s.LoadPools(ctx)
	if err != nil {
		t.Errorf("LoadPools: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LoadPools returned %d pools, want 0", len(got))
	}
	if s.Name() == "" {
		t.Error("Name() is empty")
	}
}

func TestNoopStoreSatisfiesInterface(t *testing.T) {
	var _ Store = NewNoopStore()
}

// A bad Redis URL is a configuration mistake and must be reported at startup,
// not swallowed into a silently degraded service.
func TestNewRedisStoreRejectsBadURL(t *testing.T) {
	if _, err := NewRedisStore("not-a-redis-url", 0); err == nil {
		t.Error("NewRedisStore with a bad URL returned nil error, want error")
	}
}
