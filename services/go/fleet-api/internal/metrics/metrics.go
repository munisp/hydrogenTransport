// Package metrics exposes Prometheus metrics for the service: a GET /metrics
// handler and an HTTP middleware counting http_requests_total{service,route,
// status} and observing http_request_duration_seconds. Scrape topology is
// provisioned in infra/observability/prometheus.yml (job h2fleet-services).
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "http_requests_total",
		Help: "Total HTTP requests handled, partitioned by service, route pattern and status code.",
	}, []string{"service", "route", "status"})
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "http_request_duration_seconds",
		Help:    "HTTP request latency in seconds, partitioned by service, route pattern and status code.",
		Buckets: prometheus.DefBuckets,
	}, []string{"service", "route", "status"})
)

// Handler serves the Prometheus scrape endpoint (GET /metrics).
func Handler() http.Handler { return promhttp.Handler() }

// Middleware instruments every request except the scrape endpoint itself.
// The service argument must match the `service` label in prometheus.yml.
func Middleware(service string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			status := strconv.Itoa(ww.Status())
			requestsTotal.WithLabelValues(service, route, status).Inc()
			requestDuration.WithLabelValues(service, route, status).Observe(time.Since(start).Seconds())
		})
	}
}
