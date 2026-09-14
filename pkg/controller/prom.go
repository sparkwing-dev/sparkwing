package controller

import (
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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

	principalThrottledTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_principal_throttled_total",
			Help: "Requests refused by a per-principal request budget, by route class (claim, heartbeat).",
		},
		[]string{"route_class"},
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

	claimWaitSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "sparkwing_node_claim_wait_seconds",
			Help:    "Seconds a node waited between becoming claimable and its first runner taking it. A re-claim after a requeue is not observed, because the wait it would report is the previous attempt's.",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
		},
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

	liveRunnersGauge = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "sparkwing_live_runners",
			Help: "Runners that polled for a claim inside the liveness window, across every label set. Always present, so an empty fleet reads 0 rather than dropping out of the exposition.",
		},
	)

	requestsByPrincipalTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "sparkwing_requests_by_principal_total",
			Help: "Requests that authenticated, by the kind of credential behind them. Principal names and token prefixes stay out of the label set.",
		},
		[]string{"credential"},
	)
)

const (
	placementLocal = "local"
	placementCloud = "cloud"
)

const claimRoute = "/api/v1/nodes/claim"

// safety: the window sparkwing_active_runners already reports, so the two
// liveness series answer the same question.
const runnerLivenessWindow = 2 * time.Minute

const creditSampleInterval = 5 * time.Minute

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

func observePrincipalThrottled(class string) {
	principalThrottledTotal.WithLabelValues(class).Inc()
}

func observeAuthCache(result string) {
	authTokenCacheTotal.WithLabelValues(result).Inc()
}

// safety: named rather than passed inline so the drift check can ask the same
// collectors the registry serves what they export.
var sparkwingCollectors = []prometheus.Collector{
	runsTotal,
	runDurationSeconds,
	nodesClaimedTotal,
	pendingNodesGauge,
	activeRunnersGauge,
	httpRequestsTotal,
	httpRequestDurationSeconds,
	authTokenCacheTotal,
	principalThrottledTotal,
	queueDepthGauge,
	claimWaitSeconds,
	claimUnavailableTotal,
	runnersLiveGauge,
	liveRunnersGauge,
	requestsByPrincipalTotal,
	objectStoreCollector{},
	hashingBudgetCollector{},
	creditsCollector{},
	nodeSecondsCollector{},
}

func init() {
	metricsRegistry.MustRegister(sparkwingCollectors...)
	metricsRegistry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	initZeroSeries()
}

var describedNameRE = regexp.MustCompile(`fqName: "([^"]+)".*variableLabels: \{([^}]*)\}`)

// safety: read from the collectors the registry serves rather than from the
// exposition, so a metric that has minted no child yet is still checked.
func describedMetrics() map[string][]string {
	descs := make(chan *prometheus.Desc, 64)
	out := map[string][]string{}
	for _, c := range sparkwingCollectors {
		go func() {
			c.Describe(descs)
			close(descs)
		}()
		for d := range descs {
			m := describedNameRE.FindStringSubmatch(d.String())
			if m == nil {
				continue
			}
			var labels []string
			for _, l := range strings.Split(m[2], ",") {
				if l = strings.TrimSpace(l); l != "" {
					labels = append(labels, l)
				}
			}
			slices.Sort(labels)
			out[m[1]] = labels
		}
		descs = make(chan *prometheus.Desc, 64)
	}
	return out
}

// safety: a series that appears only after the first event reads as a gap in
// the dashboard and hides a metric an alert was written against, so every
// closed label set starts at zero.
func initZeroSeries() {
	for _, state := range store.QueueStates() {
		queueDepthGauge.WithLabelValues(state)
	}
	for _, result := range []string{authCacheHit, authCacheMiss, authCacheCoalesced} {
		authTokenCacheTotal.WithLabelValues(result)
	}
	for _, credential := range []string{
		store.TokenKindUser, store.TokenKindRunner, store.TokenKindService, otherLabel,
	} {
		requestsByPrincipalTotal.WithLabelValues(credential)
	}
	for _, class := range []string{budgetClassClaim, budgetClassBeat} {
		principalThrottledTotal.WithLabelValues(class)
	}
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

// safety: a label-mismatched claim bumps ready_at while placement_hold_from
// survives it, so the hold instant is the one that still says when the node
// became claimable.
func observeClaimWait(n *store.Node) {
	if n == nil || n.ClaimGeneration > 1 {
		return
	}
	from := n.PlacementHoldFrom
	if from == nil {
		from = n.ReadyAt
	}
	if from == nil {
		return
	}
	if wait := time.Since(*from); wait > 0 {
		claimWaitSeconds.Observe(wait.Seconds())
	}
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

// safety: a child left behind reports a runner that has gone and holds a series
// forever, so the sampler deletes the sets it reported last sweep and no longer
// sees rather than zeroing them.
type runnerLivenessSampler struct {
	mu       sync.Mutex
	reported map[string]struct{}
}

var liveRunners = &runnerLivenessSampler{reported: map[string]struct{}{}}

func (s *runnerLivenessSampler) sample(labelSets [][]string) {
	counts := map[string]int{}
	for _, labels := range labelSets {
		counts[runnerLabelSet(labels)]++
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	counts = boundLabelSets(counts, s.reported)
	for set := range s.reported {
		if _, still := counts[set]; !still {
			runnersLiveGauge.DeleteLabelValues(set)
		}
	}
	reported := make(map[string]struct{}, len(counts))
	total := 0
	for set, n := range counts {
		runnersLiveGauge.WithLabelValues(set).Set(float64(n))
		reported[set] = struct{}{}
		total += n
	}
	liveRunnersGauge.Set(float64(total))
	s.reported = reported
}

// safety: the busiest sets keep their identity and the tail folds into one
// series. A tie goes to the set already reported, so a newcomer invented to
// sort first cannot displace a fleet of the same size that was there first.
func boundLabelSets(counts map[string]int, reported map[string]struct{}) map[string]int {
	if len(counts) <= maxRunnerLabelSets {
		return counts
	}
	sets := slices.Collect(maps.Keys(counts))
	slices.SortFunc(sets, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		_, aKnown := reported[a]
		_, bKnown := reported[b]
		if aKnown != bKnown {
			if aKnown {
				return -1
			}
			return 1
		}
		return strings.Compare(a, b)
	})
	bounded := make(map[string]int, maxRunnerLabelSets)
	for _, set := range sets[:maxRunnerLabelSets-1] {
		bounded[set] = counts[set]
	}
	for _, set := range sets[maxRunnerLabelSets-1:] {
		bounded[otherLabel] += counts[set]
	}
	return bounded
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

	objectStoreBucketBytesDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_bytes",
		"Bytes the bucket holds, counted as writes happen and replaced by each measured total.",
		nil, nil,
	)

	objectStoreBucketObjectsDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_objects",
		"Objects the bucket holds, counted as writes happen and replaced by each measured total.",
		nil, nil,
	)

	objectStoreCeilingDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_ceiling",
		"The configured bucket ceiling, by the unit it bounds. Absent while the bucket is unlimited.",
		[]string{"unit"}, nil,
	)

	objectStoreCeilingFrozenDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_ceiling_frozen",
		"1 while the bucket sits above its ceiling and object writes are refused, 0 otherwise. Sampled at scrape time.",
		nil, nil,
	)

	objectStoreCeilingFreezesDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_ceiling_freezes_total",
		"Times the bucket crossed its ceiling and began refusing object writes.",
		nil, nil,
	)

	objectStoreCeilingRefusedDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_ceiling_refused_total",
		"Object writes the bucket ceiling refused.",
		nil, nil,
	)

	objectStoreCeilingIncompleteDesc = prometheus.NewDesc(
		"sparkwing_object_store_bucket_ceiling_measurement_incomplete",
		"1 while the last bucket measurement stopped early and was discarded, so the total is the running count. Sampled at scrape time.",
		nil, nil,
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
	ch <- objectStoreBucketBytesDesc
	ch <- objectStoreBucketObjectsDesc
	ch <- objectStoreCeilingDesc
	ch <- objectStoreCeilingFrozenDesc
	ch <- objectStoreCeilingFreezesDesc
	ch <- objectStoreCeilingRefusedDesc
	ch <- objectStoreCeilingIncompleteDesc
}

func (objectStoreCollector) Collect(ch chan<- prometheus.Metric) {
	limiter, err := objectguard.Shared()
	if err != nil {
		return
	}
	state := limiter.State()
	collectCeiling(ch, state.Ceiling)
	for _, c := range state.Classes {
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

// safety: the ceiling gauges are emitted even while the bucket is unlimited, so
// an alert on a frozen bucket keeps a series to evaluate against.
func collectCeiling(ch chan<- prometheus.Metric, c objectguard.CeilingState) {
	ch <- prometheus.MustNewConstMetric(objectStoreBucketBytesDesc, prometheus.GaugeValue, float64(c.Bytes))
	ch <- prometheus.MustNewConstMetric(objectStoreBucketObjectsDesc, prometheus.GaugeValue, float64(c.Objects))
	ch <- prometheus.MustNewConstMetric(objectStoreCeilingDesc, prometheus.GaugeValue, float64(c.MaxBytes), "bytes")
	ch <- prometheus.MustNewConstMetric(objectStoreCeilingDesc, prometheus.GaugeValue, float64(c.MaxObjects), "objects")
	frozen := 0.0
	if c.Frozen {
		frozen = 1
	}
	ch <- prometheus.MustNewConstMetric(objectStoreCeilingFrozenDesc, prometheus.GaugeValue, frozen)
	ch <- prometheus.MustNewConstMetric(objectStoreCeilingFreezesDesc, prometheus.CounterValue, float64(c.Freezes))
	ch <- prometheus.MustNewConstMetric(objectStoreCeilingRefusedDesc, prometheus.CounterValue, float64(c.Refused))
	incomplete := 0.0
	if c.Incomplete {
		incomplete = 1
	}
	ch <- prometheus.MustNewConstMetric(objectStoreCeilingIncompleteDesc, prometheus.GaugeValue, incomplete)
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
		"Micro-credits left to spend. Sampled from the ledger on the credit-sample timer.",
		nil, nil,
	)

	creditsGrantedDesc = prometheus.NewDesc(
		"sparkwing_credits_granted_micro_total",
		"Micro-credits granted over the ledger's life, by whether the operator paid for the grant.",
		[]string{"kind"}, nil,
	)

	creditsReversedDesc = prometheus.NewDesc(
		"sparkwing_credits_reversed_micro_total",
		"Micro-credits refunded payments took back out of the ledger, reported positive.",
		nil, nil,
	)

	creditsReservedDesc = prometheus.NewDesc(
		"sparkwing_credits_reserved_micro_total",
		"Micro-credits claims reserved up front. Only a metered credential reserves, so every micro-credit here is paid work.",
		nil, nil,
	)

	creditsChargedDesc = prometheus.NewDesc(
		"sparkwing_credits_charged_micro_total",
		"Micro-credits execution billed.",
		nil, nil,
	)

	creditsRefundedDesc = prometheus.NewDesc(
		"sparkwing_credits_refunded_micro_total",
		"Micro-credits returned from the unused tail of a claim reservation.",
		nil, nil,
	)

	creditsStorageDesc = prometheus.NewDesc(
		"sparkwing_credits_storage_micro_total",
		"Micro-credits retained bytes billed. This series buys no runner time, so it is the part of the spend the charged series does not carry.",
		nil, nil,
	)

	nodeSecondsDesc = prometheus.NewDesc(
		"sparkwing_node_seconds_total",
		"Node execution seconds, by placement. The cloud series is the seconds the ledger has finished charging for, which is the billing line; the local series counts what this controller process settled for unmetered credentials.",
		[]string{"placement"}, nil,
	)
)

// safety: the sampler reads the ledger on a timer and the collector reports
// what it last saw, so a scrape never opens a database transaction, and the
// totals survive a restart that would reset an in-process counter.
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
	ch <- creditsReversedDesc
	ch <- creditsReservedDesc
	ch <- creditsChargedDesc
	ch <- creditsRefundedDesc
	ch <- creditsStorageDesc
}

func (creditsCollector) Collect(ch chan<- prometheus.Metric) {
	t := ledgerSnapshot.get()
	ch <- prometheus.MustNewConstMetric(creditsBalanceDesc, prometheus.GaugeValue, float64(t.BalanceMicro))
	ch <- prometheus.MustNewConstMetric(creditsGrantedDesc, prometheus.CounterValue,
		float64(t.GrantedFreeMicro), store.CreditGrantFree)
	ch <- prometheus.MustNewConstMetric(creditsGrantedDesc, prometheus.CounterValue,
		float64(t.GrantedPaidMicro), store.CreditGrantPaid)
	ch <- prometheus.MustNewConstMetric(creditsReversedDesc, prometheus.CounterValue,
		float64(t.ReversedMicro))
	ch <- prometheus.MustNewConstMetric(creditsReservedDesc, prometheus.CounterValue, float64(t.ReservedMicro))
	ch <- prometheus.MustNewConstMetric(creditsChargedDesc, prometheus.CounterValue, float64(t.ChargedMicro))
	ch <- prometheus.MustNewConstMetric(creditsRefundedDesc, prometheus.CounterValue, float64(t.RefundedMicro))
	ch <- prometheus.MustNewConstMetric(creditsStorageDesc, prometheus.CounterValue, float64(t.StorageMicro))
}

// safety: the cloud series is read from the ledger rather than counted in the
// request path, so a node the credit reaper cancels or a lease expiry requeues
// still reports the seconds it was billed for.
type nodeSecondsCollector struct{}

var localNodeSeconds atomic.Uint64

func addLocalNodeSeconds(seconds float64) {
	if seconds <= 0 {
		return
	}
	localNodeSeconds.Add(uint64(seconds * 1000))
}

func (nodeSecondsCollector) Describe(ch chan<- *prometheus.Desc) { ch <- nodeSecondsDesc }

func (nodeSecondsCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(nodeSecondsDesc, prometheus.CounterValue,
		float64(localNodeSeconds.Load())/1000, placementLocal)
	ch <- prometheus.MustNewConstMetric(nodeSecondsDesc, prometheus.CounterValue,
		float64(ledgerSnapshot.get().SettledSeconds), placementCloud)
}
