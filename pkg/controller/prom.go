package controller

import (
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Cardinality rule: `pipeline` is bounded and safe. Never label with
// `node_id` or `principal` -- both are effectively unbounded.
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

	// `route` is a normalized path pattern (ids/keys replaced with
	// placeholders); raw URL paths never enter a label.
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
)

func init() {
	metricsRegistry.MustRegister(
		runsTotal,
		runDurationSeconds,
		nodesClaimedTotal,
		pendingNodesGauge,
		activeRunnersGauge,
		httpRequestsTotal,
		httpRequestDurationSeconds,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
}

// metricsHandler serves /metrics from the controller's custom registry
// plus the standard Go runtime + process collectors. Safe to route
// unauthenticated -- no principal values or per-node cardinality.
func metricsHandler() http.Handler {
	return promhttp.HandlerFor(metricsRegistry, promhttp.HandlerOpts{})
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

// observeHTTPRequest records one HTTP request's outcome against the
// httpRequestsTotal counter and httpRequestDurationSeconds histogram.
// Callers must pass a pre-normalized route (see normalizeRoute).
func observeHTTPRequest(route, method string, status int, d time.Duration) {
	if route == "" {
		route = "unknown"
	}
	httpRequestsTotal.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	if d > 0 {
		httpRequestDurationSeconds.WithLabelValues(route, method).Observe(d.Seconds())
	}
}

var (
	rxRunSegment        = regexp.MustCompile(`/runs/[^/]+`)
	rxNodeSegment       = regexp.MustCompile(`/nodes/[^/]+`)
	rxTokenSegment      = regexp.MustCompile(`/tokens/[^/]+`)
	rxUserSegment       = regexp.MustCompile(`/users/[^/]+`)
	rxSecretSegment     = regexp.MustCompile(`/secrets/[^/]+`)
	rxTriggerSegment    = regexp.MustCompile(`/triggers/[^/]+`)
	rxLockSegment       = regexp.MustCompile(`/locks/[^/]+`)
	rxPipelineSegment   = regexp.MustCompile(`/pipelines/[^/]+`)
	rxWebhookGithubSeg  = regexp.MustCompile(`^/webhooks/github/[^/]+`)
	rxMultiSlashCleanup = regexp.MustCompile(`/{2,}`)
)

// normalizeRoute collapses variable-identifier URL segments into
// placeholders. Paths that don't match any known shape fall into the
// "other" bucket to cap unknown-route cardinality.
func normalizeRoute(path string) string {
	if path == "" {
		return "unknown"
	}
	if len(path) >= 7 && path[:7] == "/api/v1" {
		p := path
		p = rxRunSegment.ReplaceAllString(p, "/runs/{id}")
		p = rxNodeSegment.ReplaceAllString(p, "/nodes/{nodeID}")
		p = rxTokenSegment.ReplaceAllString(p, "/tokens/{prefix}")
		p = rxUserSegment.ReplaceAllString(p, "/users/{name}")
		p = rxSecretSegment.ReplaceAllString(p, "/secrets/{name}")
		p = rxTriggerSegment.ReplaceAllString(p, "/triggers/{id}")
		p = rxLockSegment.ReplaceAllString(p, "/locks/{key}")
		p = rxPipelineSegment.ReplaceAllString(p, "/pipelines/{name}")
		p = rxMultiSlashCleanup.ReplaceAllString(p, "/")
		return p
	}
	if rxWebhookGithubSeg.MatchString(path) {
		return "/webhooks/github/{pipeline}"
	}
	switch path {
	case "/metrics", "/":
		return path
	}
	return "other"
}
