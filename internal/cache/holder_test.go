package cache

import (
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/graph"
)

func pow10(n int) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(n)), nil)
}

var (
	weth = dex.Token{Address: "0xweth", Symbol: "WETH", Decimals: 18}
	usdc = dex.Token{Address: "0xusdc", Symbol: "USDC", Decimals: 6}
)

func samplePools(n int) []dex.Pool {
	out := make([]dex.Pool, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, dex.Pool{
			Address:  "0xpool" + string(rune('a'+i)),
			Protocol: dex.SushiswapV2,
			Token0:   weth,
			Token1:   usdc,
			FeeBps:   30,
			Reserve0: new(big.Int).Mul(big.NewInt(int64(1000+i)), pow10(18)),
			Reserve1: new(big.Int).Mul(big.NewInt(2_000_000), pow10(6)),
		})
	}
	return out
}

func TestHolderEmptyBeforeFirstSet(t *testing.T) {
	h := NewHolder()

	if got := h.Get(); got != nil {
		t.Errorf("Get() = %v on a fresh holder, want nil", got)
	}
	if !h.IsEmpty() {
		t.Error("IsEmpty() = false on a fresh holder, want true")
	}
	// Age on an empty holder must be large enough that a health check treats
	// the service as not ready, rather than accidentally reporting zero.
	if h.Age() < time.Hour*24*365 {
		t.Errorf("Age() = %v on an empty holder, want an effectively infinite age", h.Age())
	}
}

func TestHolderSetAndGet(t *testing.T) {
	h := NewHolder()
	s := graph.BuildSnapshot(samplePools(3), time.Now())

	h.Set(s)

	got := h.Get()
	if got == nil {
		t.Fatal("Get() = nil after Set")
	}
	if got.EdgeCount() != s.EdgeCount() {
		t.Errorf("EdgeCount() = %d, want %d", got.EdgeCount(), s.EdgeCount())
	}
	if h.IsEmpty() {
		t.Error("IsEmpty() = true after Set, want false")
	}
	if h.Age() > time.Second {
		t.Errorf("Age() = %v just after Set, want near zero", h.Age())
	}
}

func TestHolderIgnoresNilSet(t *testing.T) {
	h := NewHolder()
	s := graph.BuildSnapshot(samplePools(2), time.Now())
	h.Set(s)

	h.Set(nil)

	if h.Get() == nil {
		t.Error("Set(nil) cleared the holder; a failed refresh must leave the previous snapshot serving")
	}
}

// The whole point of the holder is that readers never lock and never see a
// partially built graph. Run it under -race with readers and a writer.
func TestHolderConcurrentReadersSeeConsistentSnapshots(t *testing.T) {
	h := NewHolder()
	h.Set(graph.BuildSnapshot(samplePools(2), time.Now()))

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Writer, publishing snapshots of alternating sizes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			n := 2
			if i%2 == 1 {
				n = 5
			}
			h.Set(graph.BuildSnapshot(samplePools(n), time.Now()))
		}
	}()

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				s := h.Get()
				if s == nil {
					t.Error("Get() = nil while a snapshot was published")
					return
				}
				// A snapshot is either the 2-pool or the 5-pool one, never a
				// mixture. Anything else means a torn read.
				pools := len(s.Pools())
				if pools != 2 && pools != 5 {
					t.Errorf("observed a torn snapshot with %d pools", pools)
					return
				}
				if s.EdgeCount() != pools*2 {
					t.Errorf("snapshot with %d pools has %d edges, want %d", pools, s.EdgeCount(), pools*2)
					return
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
