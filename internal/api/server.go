// Package api serves the HTTP surface.
//
// Handlers read the current graph snapshot through the holder and never touch
// the network, Redis, or a lock. Everything slow happens in the indexer, on a
// different goroutine, on a different schedule.
package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/khangpt2k6/dex-aggregator/internal/cache"
	"github.com/khangpt2k6/dex-aggregator/internal/config"
	"github.com/khangpt2k6/dex-aggregator/internal/indexer"
	"github.com/khangpt2k6/dex-aggregator/internal/rpc"
)

// RPCStatsFunc reports worker pool counters and breaker state.
//
// It is a function rather than a dependency on *rpc.Pool so that the api
// package stays unaware of how pool state is fetched, and so that simulated
// mode can simply not provide one.
type RPCStatsFunc func() (rpc.PoolStats, rpc.State)

// Server wires handlers to the snapshot holder and the indexer.
type Server struct {
	holder   *cache.Holder
	indexer  *indexer.Indexer
	cfg      *config.Config
	log      *slog.Logger
	metrics  *metrics
	hub      *hub
	rpcStats RPCStatsFunc
}

// New returns a server. Call Handler for something to pass to http.Server.
func New(h *cache.Holder, ix *indexer.Indexer, cfg *config.Config) *Server {
	return &Server{
		holder:  h,
		indexer: ix,
		cfg:     cfg,
		log:     slog.Default(),
		metrics: newMetrics(),
		hub:     newHub(h),
	}
}

// WithLogger sets the request logger.
func (s *Server) WithLogger(l *slog.Logger) *Server {
	if l != nil {
		s.log = l
	}
	return s
}

// WithRPCStats makes worker pool state visible on the status endpoint. Leave it
// unset in simulated mode, where there is no provider to report on.
func (s *Server) WithRPCStats(f RPCStatsFunc) *Server {
	s.rpcStats = f
	return s
}

// Handler returns the routed, middleware-wrapped handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/quote", s.handleQuote)
	mux.HandleFunc("GET /api/v1/tokens", s.handleTokens)
	mux.HandleFunc("GET /api/v1/pools", s.handlePools)
	mux.HandleFunc("GET /api/v1/arbitrage", s.handleArbitrage)
	mux.HandleFunc("GET /api/v1/status", s.handleStatus)
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.metrics.handler())
	mux.HandleFunc("GET /ws/prices", s.handleWS)

	return s.withCORS(s.withLogging(mux))
}

// Hub exposes the websocket hub so main can run its broadcast loop.
func (s *Server) Hub() *hub { return s.hub }

// withLogging records method, path, status and duration for each request.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		s.metrics.observeRequest(r.URL.Path, rec.status, elapsed)

		// Health and metrics scrapes would drown out everything else.
		if r.URL.Path != "/healthz" && r.URL.Path != "/metrics" {
			s.log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"durationMs", elapsed.Milliseconds(),
			)
		}
	})
}

// withCORS allows the browser app to call the API from its own origin during
// development, where the two run on different ports.
func (s *Server) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.status = code
	r.wroteHeader = true
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// Hijack lets the websocket upgrade through the logging wrapper.
//
// gorilla/websocket type-asserts the ResponseWriter to http.Hijacker directly
// rather than going through http.ResponseController, so Unwrap alone is not
// enough and every upgrade would fail with a 500.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("api: %T does not support hijacking", r.ResponseWriter)
	}
	return hijacker.Hijack()
}

// ErrorResponse is the shape of every error this API returns.
type ErrorResponse struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The status line is already sent, so the only useful thing left is to
		// record why the body is truncated.
		slog.Default().Error("encoding response failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg, detail string) {
	writeJSON(w, status, ErrorResponse{Error: msg, Detail: detail})
}

// normalizePath groups metric labels so that unbounded query strings do not
// create unbounded label cardinality.
func normalizePath(path string) string {
	if strings.HasPrefix(path, "/api/v1/") || path == "/healthz" || path == "/metrics" || path == "/ws/prices" {
		return path
	}
	return "other"
}
