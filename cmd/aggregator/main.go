// Command aggregator serves the DEX aggregator API.
//
// It starts with no configuration at all, against deterministic simulated pool
// state. Setting ETH_RPC_URL switches it to live mainnet reads.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/api"
	"github.com/khangpt2k6/dex-aggregator/internal/cache"
	"github.com/khangpt2k6/dex-aggregator/internal/config"
	"github.com/khangpt2k6/dex-aggregator/internal/dex"
	"github.com/khangpt2k6/dex-aggregator/internal/dex/sim"
	"github.com/khangpt2k6/dex-aggregator/internal/indexer"
)

// shutdownGrace is how long in-flight requests get to finish on shutdown.
const shutdownGrace = 10 * time.Second

// priceTickInterval is how often the websocket stream publishes.
const priceTickInterval = 2 * time.Second

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// Cancelled on SIGINT or SIGTERM, which unwinds the indexer, the price
	// stream, and the HTTP server in that order.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	store := openStore(ctx, cfg, log)
	if closer, ok := store.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}

	sources, err := buildSources(cfg, log)
	if err != nil {
		return err
	}

	holder := cache.NewHolder()
	ix := indexer.New(sources, holder, store, cfg.RefreshInterval).WithLogger(log)

	log.Info("starting",
		"mode", modeName(cfg),
		"addr", cfg.HTTPAddr,
		"store", store.Name(),
		"sources", ix.Status().SourceNames,
		"refreshInterval", cfg.RefreshInterval.String(),
		"maxHops", cfg.MaxHops,
	)

	// Serve stored state first, so a restart is useful before its own refresh
	// finishes.
	if err := ix.WarmStart(ctx); err != nil {
		log.Warn("warm start failed", "err", err)
	}

	// Then index once synchronously, so the first request never meets an empty
	// snapshot and a 503.
	if err := ix.RefreshOnce(ctx); err != nil {
		log.Warn("initial refresh failed, starting anyway", "err", err)
	}

	srv := api.New(holder, ix, cfg).WithLogger(log)

	go ix.Run(ctx)
	go srv.Hub().Run(ctx, priceTickInterval)

	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: srv.Handler(),

		ReadHeaderTimeout: 5 * time.Second,
		// Generous because the websocket endpoint shares this server; the
		// quote path is bounded by its own handler work, not by this.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down", "grace", shutdownGrace.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return err
	}

	log.Info("stopped cleanly")
	return nil
}

// buildSources picks live or simulated pool sources.
//
// This is the only place in the program that knows the difference. Everything
// downstream sees dex.PoolSource and behaves identically either way.
func buildSources(cfg *config.Config, log *slog.Logger) ([]dex.PoolSource, error) {
	if !cfg.LiveMode() {
		log.Info("no ETH_RPC_URL set, using deterministic simulated pools", "seed", cfg.SimSeed)
		return []dex.PoolSource{sim.New(cfg.SimSeed)}, nil
	}

	// Live on-chain sources are wired in a follow-up change; until then a
	// configured RPC URL runs against simulated data rather than silently
	// serving nothing.
	log.Warn("ETH_RPC_URL is set but on-chain sources are not wired yet, falling back to simulated pools")
	return []dex.PoolSource{sim.New(cfg.SimSeed)}, nil
}

// openStore connects to Redis if configured, and degrades to no persistence if
// it is unreachable.
//
// Redis being down should cost a slower cold start, not an outage. It is not on
// the request path, so the service is fully functional without it.
func openStore(ctx context.Context, cfg *config.Config, log *slog.Logger) cache.Store {
	if cfg.RedisURL == "" {
		return cache.NewNoopStore()
	}

	rs, err := cache.NewRedisStore(cfg.RedisURL, 10*time.Minute)
	if err != nil {
		log.Warn("redis url is invalid, continuing without persistence", "err", err)
		return cache.NewNoopStore()
	}

	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	if err := rs.Ping(pingCtx); err != nil {
		log.Warn("redis is unreachable, continuing without persistence", "err", err)
		_ = rs.Close()
		return cache.NewNoopStore()
	}

	log.Info("connected to redis")
	return rs
}

func modeName(cfg *config.Config) string {
	if cfg.LiveMode() {
		return "live"
	}
	return "simulated"
}
