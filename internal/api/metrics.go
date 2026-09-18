package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// metrics holds the collectors this service exposes.
//
// They live in their own registry rather than the global default, so that
// importing this package never has a side effect on someone else's metrics and
// two servers can exist in one test binary without a duplicate registration
// panic.
type metrics struct {
	registry *prometheus.Registry

	requestDuration *prometheus.HistogramVec
	requestTotal    *prometheus.CounterVec
	routerDuration  prometheus.Histogram
}

func newMetrics() *metrics {
	reg := prometheus.NewRegistry()

	m := &metrics{
		registry: reg,

		requestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dexagg_http_request_duration_seconds",
			Help: "HTTP request duration by path.",
			// Buckets are clustered at the low end because everything
			// interesting about this service happens under 10ms.
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.5, 1},
		}, []string{"path", "status"}),

		requestTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dexagg_http_requests_total",
			Help: "HTTP requests by path and status.",
		}, []string{"path", "status"}),

		routerDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: "dexagg_router_duration_seconds",
			Help: "Time spent searching the graph, excluding HTTP overhead. This is the figure the 10ms budget refers to.",
			// The budget is 10ms, so the buckets need resolution on both sides
			// of it to make a breach visible rather than merely present.
			Buckets: []float64{0.0001, 0.00025, 0.0005, 0.001, 0.0025, 0.005, 0.0075, 0.01, 0.02, 0.05, 0.1},
		}),
	}

	reg.MustRegister(m.requestDuration, m.requestTotal, m.routerDuration)
	return m
}

func (m *metrics) observeRequest(path string, status int, d time.Duration) {
	p := normalizePath(path)
	s := strconv.Itoa(status)

	m.requestDuration.WithLabelValues(p, s).Observe(d.Seconds())
	m.requestTotal.WithLabelValues(p, s).Inc()
}

func (m *metrics) observeRouter(d time.Duration) {
	m.routerDuration.Observe(d.Seconds())
}

func (m *metrics) handler() http.HandlerFunc {
	h := promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
	return func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }
}
