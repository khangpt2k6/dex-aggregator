package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/khangpt2k6/dex-aggregator/internal/amm"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
)

// Store persists pool state between refreshes and between instances.
//
// It is deliberately not on the request path. A quote reads the in-memory
// snapshot; this exists so a cold instance can serve traffic before its first
// RPC refresh finishes, and so several instances do not each pay full price to
// index the same pools.
type Store interface {
	Name() string
	SavePools(ctx context.Context, pools []dex.Pool) error
	LoadPools(ctx context.Context) ([]dex.Pool, error)
}

// poolsKey is the single key the store writes. Pool state is only useful as a
// consistent set, so it is written and read whole rather than per pool.
const poolsKey = "dexagg:pools:v1"

// NoopStore satisfies Store without persisting anything.
//
// Redis is optional. Running without it costs a slower cold start and no
// cross-instance sharing, which is a fine trade for a local run and not worth
// failing startup over.
type NoopStore struct{}

// NewNoopStore returns a store that discards everything.
func NewNoopStore() *NoopStore { return &NoopStore{} }

func (NoopStore) Name() string { return "none" }

func (NoopStore) SavePools(context.Context, []dex.Pool) error { return nil }

func (NoopStore) LoadPools(context.Context) ([]dex.Pool, error) { return nil, nil }

// RedisStore persists pool state in Redis.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStore connects to Redis.
//
// A malformed URL is a configuration error and is reported here, at startup,
// rather than becoming a mysterious runtime failure. An unreachable server is
// a different matter and is handled by the caller, which falls back to the
// noop store.
func NewRedisStore(url string, ttl time.Duration) (*RedisStore, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("cache: parsing redis url: %w", err)
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &RedisStore{client: redis.NewClient(opts), ttl: ttl}, nil
}

func (RedisStore) Name() string { return "redis" }

// Ping checks that the server is actually reachable.
func (s *RedisStore) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

// Close releases the connection pool.
func (s *RedisStore) Close() error { return s.client.Close() }

// SavePools writes the pool set with a TTL, so that state from a dead instance
// expires instead of being served as though it were current.
func (s *RedisStore) SavePools(ctx context.Context, pools []dex.Pool) error {
	blob, err := encodePools(pools)
	if err != nil {
		return err
	}
	if err := s.client.Set(ctx, poolsKey, blob, s.ttl).Err(); err != nil {
		return fmt.Errorf("cache: writing pools: %w", err)
	}
	return nil
}

// LoadPools reads the stored pool set. A missing key is not an error; it just
// means nothing has indexed yet.
func (s *RedisStore) LoadPools(ctx context.Context) ([]dex.Pool, error) {
	blob, err := s.client.Get(ctx, poolsKey).Bytes()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache: reading pools: %w", err)
	}
	return decodePools(blob)
}

// poolDTO is the wire form of a pool.
//
// Amounts are strings, not JSON numbers. A JSON number is a float64 to most
// parsers, which silently corrupts anything past 2^53, and pool reserves at 18
// decimals are routinely far past that. Encoding money as a float is how you
// get a quote that is wrong by an amount nobody can explain.
type poolDTO struct {
	Address   string       `json:"address"`
	Protocol  dex.Protocol `json:"protocol"`
	Token0    dex.Token    `json:"token0"`
	Token1    dex.Token    `json:"token1"`
	FeeBps    uint32       `json:"feeBps"`
	Simulated bool         `json:"simulated"`

	Reserve0 string `json:"reserve0,omitempty"`
	Reserve1 string `json:"reserve1,omitempty"`

	SqrtPriceX96 string    `json:"sqrtPriceX96,omitempty"`
	Liquidity    string    `json:"liquidity,omitempty"`
	Tick         int32     `json:"tick,omitempty"`
	TickSpacing  int32     `json:"tickSpacing,omitempty"`
	Ticks        []tickDTO `json:"ticks,omitempty"`
}

type tickDTO struct {
	Index        int32  `json:"index"`
	LiquidityNet string `json:"liquidityNet"`
}

func encodePools(pools []dex.Pool) ([]byte, error) {
	out := make([]poolDTO, 0, len(pools))

	for _, p := range pools {
		d := poolDTO{
			Address:      p.Address,
			Protocol:     p.Protocol,
			Token0:       p.Token0,
			Token1:       p.Token1,
			FeeBps:       p.FeeBps,
			Simulated:    p.Simulated,
			Reserve0:     bigToString(p.Reserve0),
			Reserve1:     bigToString(p.Reserve1),
			SqrtPriceX96: bigToString(p.SqrtPriceX96),
			Liquidity:    bigToString(p.Liquidity),
			Tick:         p.Tick,
			TickSpacing:  p.TickSpacing,
		}
		for _, tk := range p.Ticks {
			d.Ticks = append(d.Ticks, tickDTO{
				Index:        tk.Index,
				LiquidityNet: bigToString(tk.LiquidityNet),
			})
		}
		out = append(out, d)
	}

	blob, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("cache: encoding pools: %w", err)
	}
	return blob, nil
}

func decodePools(blob []byte) ([]dex.Pool, error) {
	var in []poolDTO
	if err := json.Unmarshal(blob, &in); err != nil {
		return nil, fmt.Errorf("cache: decoding pools: %w", err)
	}

	out := make([]dex.Pool, 0, len(in))
	for _, d := range in {
		p := dex.Pool{
			Address:     d.Address,
			Protocol:    d.Protocol,
			Token0:      d.Token0,
			Token1:      d.Token1,
			FeeBps:      d.FeeBps,
			Simulated:   d.Simulated,
			Tick:        d.Tick,
			TickSpacing: d.TickSpacing,
		}

		var err error
		if p.Reserve0, err = stringToBig(d.Reserve0, "reserve0", d.Address); err != nil {
			return nil, err
		}
		if p.Reserve1, err = stringToBig(d.Reserve1, "reserve1", d.Address); err != nil {
			return nil, err
		}
		if p.SqrtPriceX96, err = stringToBig(d.SqrtPriceX96, "sqrtPriceX96", d.Address); err != nil {
			return nil, err
		}
		if p.Liquidity, err = stringToBig(d.Liquidity, "liquidity", d.Address); err != nil {
			return nil, err
		}

		for _, td := range d.Ticks {
			net, err := stringToBig(td.LiquidityNet, "liquidityNet", d.Address)
			if err != nil {
				return nil, err
			}
			p.Ticks = append(p.Ticks, amm.V3Tick{Index: td.Index, LiquidityNet: net})
		}

		out = append(out, p)
	}
	return out, nil
}

func bigToString(n *big.Int) string {
	if n == nil {
		return ""
	}
	return n.String()
}

func stringToBig(s, field, pool string) (*big.Int, error) {
	if s == "" {
		return nil, nil
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("cache: pool %s has invalid %s %q", pool, field, s)
	}
	return n, nil
}
