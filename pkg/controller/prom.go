package controller

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/sparkwing-dev/sparkwing/internal/objectguard"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

var (
	metricsRegistry = prometheus.NewRegistry()

	runsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_runs_total",
			Help: "Runs that reached a terminal state, partitioned by pipeline and terminal status.",
		},
		[]string{"pipeline", "status"},
	)

	runDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sparkwing_run_duration_seconds",
			Help:    "End-to-end wall time from CreateRun to FinishRun.",
			Buckets: []float64{1, 5, 10, 30, 60, 300, 900, 1800},
		},
		[]string{"pipeline", "outcome"},
	)

	nodesClaimedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_nodes_claimed_total",
			Help: "Successful node claims from the warm-pool / agent claim endpoint.",
		},
		[]string{"pipeline"},
	)

	pendingNodesGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "sparkwing_pending_nodes",
			Help: "Nodes with ready_at set and claimed_by null (claim-queue depth). Sampled from the reaper loop.",
		},
	)

	activeRunnersGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "sparkwing_active_runners",
			Help: "Distinct runners that held a claim with a non-expired lease in the last 2 minutes. Sampled from the reaper loop.",
		},
	)

	httpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_http_requests_total",
			Help: "HTTP requests handled by the controller, by normalized route, method, and status code.",
		},
		[]string{"route", "method", "status"},
	)

	httpRequestDurationSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sparkwing_http_request_duration_seconds",
			Help:    "HTTP request handling latency, by normalized route and method.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{"route", "method"},
	)

	authTokenCacheTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_auth_token_cache_total",
			Help: "Bearer verifications by how the verified-token cache answered them: hit, miss, or coalesced onto a verification already in flight for the same token.",
		},
		[]string{"result"},
	)
)

const (
	authCacheHit       = "hit"
	authCacheMiss      = "miss"
	authCacheCoalesced = "coalesced"
)

func observeAuthCache(result string) {
	authTokenCacheTotal.WithLabelValues(result).Inc()
}

func init() {
	metricsRegistry.MustRegister(
		runsTotal,
		runDurationSeconds,
		nodesClaimedTotal,
		pendingNodesGauge,
		activeRunnersGauge,
		httpRequestsTotal,
		httpRequestDurationSeconds,
		authTokenCacheTotal,
		objectStoreCollector{},
		hashingBudgetCollector{},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

func metricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
}

func (s *Server) metricsServer() *http.Server {
	if s.metricsAddr == "" {
		return nil
	}
	metricsMux := http.NewServeMux()
	metricsMux.Handle("GET /metrics", metricsHandler())
	return &http.Server{
		Addr:              s.metricsAddr,
		Handler:           metricsMux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
}

func observeRunFinish(pipeline, status string, duration time.Duration) {
	if pipeline == "" {
		pipeline = "unknown"
	}
	runsTotal.WithLabelValues(pipeline, status).Inc()
	if duration > 0 {
		runDurationSeconds.WithLabelValues(pipeline, status).Observe(duration.Seconds())
	}
}

func observeNodeClaim(pipeline string) {
	if pipeline == "" {
		pipeline = "unknown"
	}
	nodesClaimedTotal.WithLabelValues(pipeline).Inc()
}

func setPendingNodes(n int)  { pendingNodesGauge.Set(float64(n)) }
func setActiveRunners(n int) { activeRunnersGauge.Set(float64(n)) }

func observeHTTPRequest(route, method string, status int, d time.Duration) {
	if route == "" {
		route = "unknown"
	}
	method = methodLabel(method)
	httpRequestsTotal.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	if d > 0 {
		httpRequestDurationSeconds.WithLabelValues(route, method).Observe(d.Seconds())
	}
}

// safety: a request line carries any token the caller cares to invent, so the
// method reaches Prometheus only when it is one this server can be asked for.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return otherLabel
}

const otherLabel = "other"

// safety: a request that matches no route is labeled with a constant, because
// any caller could otherwise mint a permanent time series per path they invent.
func muxRouteLabeler(muxes ...*http.ServeMux) func(*http.Request) string {
	return func(r *http.Request) string {
		for _, m := range muxes {
			if _, pattern := m.Handler(r); pattern != "" {
				if route := routeFromPattern(pattern); route != "" {
					return route
				}
			}
		}
		return otherLabel
	}
}

// safety: a mux answers a path it wants cleaned with that path as the pattern,
// and answers the catch-all registration with "/", so only a pattern carrying a
// method is a route this server named and is safe to use as a label.
func routeFromPattern(pattern string) string {
	method, rest, ok := strings.Cut(pattern, " ")
	if !ok || method == "" || !strings.HasPrefix(rest, "/") {
		return ""
	}
	return rest
}

var (
	objectStoreRequestsDesc = prometheus.NewDesc(
		"sparkwing_object_store_requests_total",
		"Object-store requests the process-wide request budget saw, by request class and whether the budget let them reach the store.",
		[]string{"class", "outcome"}, nil,
	)

	objectStoreTripsDesc = prometheus.NewDesc(
		"sparkwing_object_store_trips_total",
		"Times an object-store request class exhausted its budget and began refusing requests.",
		[]string{"class"}, nil,
	)

	objectStoreTrippedDesc = prometheus.NewDesc(
		"sparkwing_object_store_tripped",
		"1 while an object-store request class is refusing requests, 0 otherwise. Sampled at scrape time.",
		[]string{"class"}, nil,
	)
)

// safety: read at scrape time rather than mirrored, so the limiter stays the
// one place that knows how much budget is spent, and the class label takes only
// the four names objectguard mints, never a caller-supplied string.
type objectStoreCollector struct{}

func (objectStoreCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- objectStoreRequestsDesc
	ch <- objectStoreTripsDesc
	ch <- objectStoreTrippedDesc
}

func (objectStoreCollector) Collect(ch chan<- prometheus.Metric) {
	limiter, err := objectguard.Shared()
	if err != nil {
		return
	}
	for _, c := range limiter.State().Classes {
		class := string(c.Class)
		ch <- prometheus.MustNewConstMetric(objectStoreRequestsDesc, prometheus.CounterValue, float64(c.Allowed), class, "allowed")
		ch <- prometheus.MustNewConstMetric(objectStoreRequestsDesc, prometheus.CounterValue, float64(c.Refused), class, "refused")
		ch <- prometheus.MustNewConstMetric(objectStoreTripsDesc, prometheus.CounterValue, float64(c.Trips), class)
		tripped := 0.0
		if c.Tripped {
			tripped = 1
		}
		ch <- prometheus.MustNewConstMetric(objectStoreTrippedDesc, prometheus.GaugeValue, tripped, class)
	}
}

var authHashingRejectedDesc = prometheus.NewDesc(
	"sparkwing_auth_hashing_rejected_total",
	"Credential verifications the argon2id memory budget shed rather than queued, answered 503 with a Retry-After.",
	nil, nil,
)

// safety: read at scrape time from the store's own counter, so the budget stays the one place that knows what it shed.
type hashingBudgetCollector struct{}

func (hashingBudgetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- authHashingRejectedDesc
}

func (hashingBudgetCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(authHashingRejectedDesc, prometheus.CounterValue, float64(store.Argon2Shed()))
}
