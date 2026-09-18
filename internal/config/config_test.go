package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("ETH_RPC_URL", "")
	t.Setenv("REDIS_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.RPCWorkers != 16 {
		t.Errorf("RPCWorkers = %d, want 16", cfg.RPCWorkers)
	}
	if cfg.RPCRatePerSec != 10 {
		t.Errorf("RPCRatePerSec = %v, want 10", cfg.RPCRatePerSec)
	}
	if cfg.RefreshInterval != 6*time.Second {
		t.Errorf("RefreshInterval = %v, want 6s", cfg.RefreshInterval)
	}
	if cfg.MaxHops != 3 {
		t.Errorf("MaxHops = %d, want 3", cfg.MaxHops)
	}
	if cfg.LiveMode() {
		t.Error("LiveMode() = true with no ETH_RPC_URL, want false")
	}
}

func TestLiveModeFollowsRPCURL(t *testing.T) {
	t.Setenv("ETH_RPC_URL", "https://example.invalid/v2/key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.LiveMode() {
		t.Error("LiveMode() = false with ETH_RPC_URL set, want true")
	}
}

func TestLoadOverridesFromEnv(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":9999")
	t.Setenv("RPC_WORKERS", "64")
	t.Setenv("RPC_RATE_PER_SEC", "2.5")
	t.Setenv("REFRESH_INTERVAL", "250ms")
	t.Setenv("MAX_HOPS", "4")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.HTTPAddr != ":9999" {
		t.Errorf("HTTPAddr = %q, want :9999", cfg.HTTPAddr)
	}
	if cfg.RPCWorkers != 64 {
		t.Errorf("RPCWorkers = %d, want 64", cfg.RPCWorkers)
	}
	if cfg.RPCRatePerSec != 2.5 {
		t.Errorf("RPCRatePerSec = %v, want 2.5", cfg.RPCRatePerSec)
	}
	if cfg.RefreshInterval != 250*time.Millisecond {
		t.Errorf("RefreshInterval = %v, want 250ms", cfg.RefreshInterval)
	}
	if cfg.MaxHops != 4 {
		t.Errorf("MaxHops = %d, want 4", cfg.MaxHops)
	}
}

func TestLoadRejectsBadValues(t *testing.T) {
	cases := []struct{ key, val string }{
		{"RPC_WORKERS", "0"},
		{"RPC_WORKERS", "not-a-number"},
		{"RPC_RATE_PER_SEC", "-1"},
		{"MAX_HOPS", "0"},
		{"MAX_HOPS", "9"},
		{"REFRESH_INTERVAL", "banana"},
	}

	for _, c := range cases {
		t.Run(c.key+"="+c.val, func(t *testing.T) {
			t.Setenv(c.key, c.val)
			if _, err := Load(); err == nil {
				t.Errorf("Load() with %s=%s returned nil error, want error", c.key, c.val)
			}
		})
	}
}
