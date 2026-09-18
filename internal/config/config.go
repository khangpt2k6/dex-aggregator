// Package config parses process configuration from the environment.
//
// Every setting has a working default, so the service starts with no
// environment at all and runs against simulated pool data. Supplying
// ETH_RPC_URL is what switches it to live mainnet reads.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the fully resolved settings for one process.
type Config struct {
	// HTTPAddr is the listen address for the API server.
	HTTPAddr string

	// RedisURL is the shared snapshot store. Empty disables Redis and the
	// service falls back to an in-memory-only store.
	RedisURL string

	// ETHRPCURL is the JSON-RPC endpoint. Empty selects simulated pools.
	ETHRPCURL string

	// RPCWorkers bounds how many RPC calls may be in flight at once.
	RPCWorkers int

	// RPCRatePerSec is the sustained request rate allowed to the provider.
	RPCRatePerSec float64

	// RPCBurst is how far the rate limiter may run ahead of the sustained rate.
	RPCBurst int

	// RefreshInterval is how often the indexer rebuilds the graph snapshot.
	RefreshInterval time.Duration

	// MaxHops caps route length. Longer routes cost more gas than they save.
	MaxHops int

	// SimSeed makes simulated pool state reproducible across runs and machines.
	SimSeed int64
}

// LiveMode reports whether pool state comes from a real node rather than the
// deterministic simulator.
func (c *Config) LiveMode() bool {
	return strings.TrimSpace(c.ETHRPCURL) != ""
}

// Load reads configuration from the environment, applying defaults for
// anything unset. It returns an error rather than silently correcting a value
// the operator got wrong.
func Load() (*Config, error) {
	cfg := &Config{
		HTTPAddr:        envString("HTTP_ADDR", ":8080"),
		RedisURL:        envString("REDIS_URL", ""),
		ETHRPCURL:       envString("ETH_RPC_URL", ""),
		RPCWorkers:      16,
		RPCRatePerSec:   10,
		RPCBurst:        20,
		RefreshInterval: 6 * time.Second,
		MaxHops:         3,
		SimSeed:         1337,
	}

	var err error

	if cfg.RPCWorkers, err = envInt("RPC_WORKERS", cfg.RPCWorkers); err != nil {
		return nil, err
	}
	if cfg.RPCBurst, err = envInt("RPC_BURST", cfg.RPCBurst); err != nil {
		return nil, err
	}
	if cfg.MaxHops, err = envInt("MAX_HOPS", cfg.MaxHops); err != nil {
		return nil, err
	}
	if cfg.RPCRatePerSec, err = envFloat("RPC_RATE_PER_SEC", cfg.RPCRatePerSec); err != nil {
		return nil, err
	}
	if cfg.RefreshInterval, err = envDuration("REFRESH_INTERVAL", cfg.RefreshInterval); err != nil {
		return nil, err
	}
	if cfg.SimSeed, err = envInt64("SIM_SEED", cfg.SimSeed); err != nil {
		return nil, err
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.RPCWorkers < 1 {
		return fmt.Errorf("config: RPC_WORKERS must be at least 1, got %d", c.RPCWorkers)
	}
	if c.RPCBurst < 1 {
		return fmt.Errorf("config: RPC_BURST must be at least 1, got %d", c.RPCBurst)
	}
	if c.RPCRatePerSec <= 0 {
		return fmt.Errorf("config: RPC_RATE_PER_SEC must be positive, got %v", c.RPCRatePerSec)
	}
	if c.RefreshInterval <= 0 {
		return fmt.Errorf("config: REFRESH_INTERVAL must be positive, got %v", c.RefreshInterval)
	}
	// Above 6 hops the search space grows faster than the routes improve, and
	// the gas cost of the extra hops exceeds any rate gain in practice.
	if c.MaxHops < 1 || c.MaxHops > 6 {
		return fmt.Errorf("config: MAX_HOPS must be between 1 and 6, got %d", c.MaxHops)
	}
	return nil
}

func envString(key, def string) string {
	if v, ok := lookup(key); ok {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v, ok := lookup(key)
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", key, v)
	}
	return n, nil
}

func envInt64(key string, def int64) (int64, error) {
	v, ok := lookup(key)
	if !ok {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be an integer, got %q", key, v)
	}
	return n, nil
}

func envFloat(key string, def float64) (float64, error) {
	v, ok := lookup(key)
	if !ok {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a number, got %q", key, v)
	}
	return f, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := lookup(key)
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("config: %s must be a duration such as 5s, got %q", key, v)
	}
	return d, nil
}

// lookup treats an empty value the same as unset, so that clearing a variable
// in a shell or compose file restores the default rather than producing a
// zero value.
func lookup(key string) (string, bool) {
	v, ok := os.LookupEnv(key)
	if !ok {
		return "", false
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return "", false
	}
	return v, true
}
