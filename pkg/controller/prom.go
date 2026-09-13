package controller

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
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

	queueDepthGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "sparkwing_queue_depth",
			Help: "Nodes short of a terminal outcome, by queue state. Sampled from the reaper loop.",
		},
		[]string{"state"},
	)

	claimWaitSeconds = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "sparkwing_node_claim_wait_seconds",
			Help:    "Seconds a node waited between becoming claimable and a runner taking it, by placement.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		},
		[]string{"placement"},
	)

	claimUnavailableTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "sparkwing_claim_unavailable_total",
			Help: "Claim requests answered 503, which a runner retries after the interval the response names.",
		},
	)

	runnersLiveGauge = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "sparkwing_runners_live",
			Help: "Runners that polled for a claim inside the liveness window, by the label set they advertised. Sampled from the reaper loop.",
		},
		[]string{"label_set"},
	)

	nodeSecondsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_node_seconds_total",
			Help: "Node execution seconds the controller settled, by placement. The cloud series is the billing line.",
		},
		[]string{"placement"},
	)

	requestsByPrincipalTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_requests_by_principal_total",
			Help: "Requests that authenticated, by the kind of credential behind them. Principal names and token prefixes stay out of the label set.",
		},
		[]string{"credential"},
	)
)

// safety: the split a bill is drawn from, so the two values are the only ones
// the placement label ever carries.
const (
	placementLocal = "local"
	placementCloud = "cloud"
)

// safety: a metered credential is paid and every other one runs free, so the
// credit metrics carry a closed pair rather than an identity.
const (
	principalKindFree = "free"
	principalKindPaid = "paid"
)

const claimRoute = "/api/v1/nodes/claim"

// safety: the window sparkwing_active_runners already reports, so the two
// liveness series answer the same question.
const runnerLivenessWindow = 2 * time.Minute

// safety: a runner names its own labels, so past this many distinct sets the
// rest collapse onto one series.
const maxRunnerLabelSets = 32

// safety: keeps one label value short enough that a runner cannot make a scrape
// expensive by advertising a long list.
const maxRunnerLabelSetBytes = 120

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
		queueDepthGauge,
		claimWaitSeconds,
		claimUnavailableTotal,
		runnersLiveGauge,
		nodeSecondsTotal,
		requestsByPrincipalTotal,
		objectStoreCollector{},
		hashingBudgetCollector{},
		creditsCollector{},
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	initZeroSeries()
}

// safety: a series that appears only after the first event reads as a gap in
// the dashboard and hides a metric an alert was written against, so every
// closed label set starts at zero.
func initZeroSeries() {
	for _, state := range store.QueueStates() {
		queueDepthGauge.WithLabelValues(state)
	}
	for _, placement := range []string{placementLocal, placementCloud} {
		claimWaitSeconds.WithLabelValues(placement)
		nodeSecondsTotal.WithLabelValues(placement)
	}
	for _, result := range []string{authCacheHit, authCacheMiss, authCacheCoalesced} {
		authTokenCacheTotal.WithLabelValues(result)
	}
	for _, credential := range []string{
		store.TokenKindUser, store.TokenKindRunner, store.TokenKindService, otherLabel,
	} {
		requestsByPrincipalTotal.WithLabelValues(credential)
	}
	runnersLiveGauge.WithLabelValues("")
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
	// safety: a claim answered 503 is a runner told to back off, which is the
	// signal an operator pages on, so it gets a series of its own rather than
	// a query over the route and status labels.
	if route == claimRoute && status == http.StatusServiceUnavailable {
		claimUnavailableTotal.Inc()
	}
}

func setQueueDepth(counts map[string]int) {
	for _, state := range store.QueueStates() {
		queueDepthGauge.WithLabelValues(state).Set(float64(counts[state]))
	}
}

func observeClaimWait(placement string, readyAt *time.Time) {
	if readyAt == nil {
		return
	}
	if wait := time.Since(*readyAt); wait > 0 {
		claimWaitSeconds.WithLabelValues(placement).Observe(wait.Seconds())
	}
}

func observeNodeSeconds(placement string, seconds float64) {
	if seconds <= 0 {
		return
	}
	nodeSecondsTotal.WithLabelValues(placement).Add(seconds)
}

func observeRequestPrincipal(kind string) {
	requestsByPrincipalTotal.WithLabelValues(credentialLabel(kind)).Inc()
}

// safety: the kind reaches this from a token row, and a peer or loopback
// principal may carry one the store never mints, so anything outside the set
// collapses rather than minting a series.
func credentialLabel(kind string) string {
	switch kind {
	case store.TokenKindUser, store.TokenKindRunner, store.TokenKindService:
		return kind
	}
	return otherLabel
}

// safety: a gauge child left behind reports a runner that has gone, so the
// sampler zeroes every set it reported last time and no longer sees.
type runnerLivenessSampler struct {
	mu     sync.Mutex
	report map[string]struct{}
}

var liveRunners = &runnerLivenessSampler{report: map[string]struct{}{"": {}}}

func (s *runnerLivenessSampler) sample(labelSets [][]string) {
	sets := make([]string, 0, len(labelSets))
	for _, labels := range labelSets {
		sets = append(sets, runnerLabelSet(labels))
	}
	slices.Sort(sets)

	counts := map[string]int{}
	for _, set := range sets {
		if _, known := counts[set]; !known && len(counts) >= maxRunnerLabelSets {
			set = otherLabel
		}
		counts[set]++
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for set := range s.report {
		if _, still := counts[set]; !still {
			runnersLiveGauge.WithLabelValues(set).Set(0)
		}
	}
	for set, n := range counts {
		runnersLiveGauge.WithLabelValues(set).Set(float64(n))
	}
	s.report = map[string]struct{}{"": {}}
	for set := range counts {
		s.report[set] = struct{}{}
	}
}

// safety: a runner asserts its own labels, so the value is ordered for a stable
// series and anything over the byte budget collapses onto the overflow series.
func runnerLabelSet(labels []string) string {
	clean := make([]string, 0, len(labels))
	for _, l := range labels {
		if l = strings.TrimSpace(l); l != "" {
			clean = append(clean, l)
		}
	}
	slices.Sort(clean)
	clean = slices.Compact(clean)
	set := strings.Join(clean, ",")
	if len(set) > maxRunnerLabelSetBytes {
		return otherLabel
	}
	return set
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

var (
	creditsBalanceDesc = prometheus.NewDesc(
		"sparkwing_credits_balance_micro",
		"Micro-credits left to spend. Sampled from the ledger by the reaper loop.",
		nil, nil,
	)

	creditsGrantedDesc = prometheus.NewDesc(
		"sparkwing_credits_granted_micro_total",
		"Micro-credits granted over the ledger's life, by whether the operator paid for the grant.",
		[]string{"kind"}, nil,
	)

	creditsReservedDesc = prometheus.NewDesc(
		"sparkwing_credits_reserved_micro_total",
		"Micro-credits claims reserved up front, by principal kind. Only a metered principal reserves, so the free series is the zero line a ratio divides by.",
		[]string{"principal_kind"}, nil,
	)

	creditsChargedDesc = prometheus.NewDesc(
		"sparkwing_credits_charged_micro_total",
		"Micro-credits execution billed, by principal kind.",
		[]string{"principal_kind"}, nil,
	)

	creditsRefundedDesc = prometheus.NewDesc(
		"sparkwing_credits_refunded_micro_total",
		"Micro-credits returned from the unused tail of a claim reservation, by principal kind.",
		[]string{"principal_kind"}, nil,
	)
)

// safety: the reaper samples the ledger and the collector reports what it last
// saw, so a scrape never opens a database transaction, and the totals survive a
// restart that would reset an in-process counter.
type creditsLedgerSnapshot struct {
	mu     sync.Mutex
	totals store.CreditLedgerTotals
}

var ledgerSnapshot = &creditsLedgerSnapshot{}

func (c *creditsLedgerSnapshot) set(t store.CreditLedgerTotals) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.totals = t
}

func (c *creditsLedgerSnapshot) get() store.CreditLedgerTotals {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totals
}

type creditsCollector struct{}

func (creditsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- creditsBalanceDesc
	ch <- creditsGrantedDesc
	ch <- creditsReservedDesc
	ch <- creditsChargedDesc
	ch <- creditsRefundedDesc
}

func (creditsCollector) Collect(ch chan<- prometheus.Metric) {
	t := ledgerSnapshot.get()
	ch <- prometheus.MustNewConstMetric(creditsBalanceDesc, prometheus.GaugeValue, float64(t.BalanceMicro))
	ch <- prometheus.MustNewConstMetric(creditsGrantedDesc, prometheus.CounterValue,
		float64(t.GrantedFreeMicro), store.CreditGrantFree)
	ch <- prometheus.MustNewConstMetric(creditsGrantedDesc, prometheus.CounterValue,
		float64(t.GrantedPaidMicro), store.CreditGrantPaid)
	for desc, paid := range map[*prometheus.Desc]int64{
		creditsReservedDesc: t.ReservedMicro,
		creditsChargedDesc:  t.ChargedMicro,
		creditsRefundedDesc: t.RefundedMicro,
	} {
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, 0, principalKindFree)
		ch <- prometheus.MustNewConstMetric(desc, prometheus.CounterValue, float64(paid), principalKindPaid)
	}
}
