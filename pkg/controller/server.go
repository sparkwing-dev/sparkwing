package controller

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Server owns the route table, the backing store, and the
// dispatcher. A single Server instance services all concurrent HTTP
// requests; the store itself serializes writes.
type Server struct {
	store  *store.Store
	logger *slog.Logger

	hostKeyScan       func(ctx context.Context, host string, port int) (ssh.PublicKey, error)
	identityLinkLimit perMinuteLimiter

	credentialIssuer           CredentialIssuer
	runnerMeteringWriter       RunnerMeteringWriter
	resolveCapabilityManager   func(context.Context, store.Team) (CapabilityManager, error)
	authorization              RouteAuthorization
	accountReads               AccountReader
	accountWrites              AccountWriter
	identityLinkWrites         IdentityLinkWriter
	dataController             *DataController
	sourceController           *SourceController
	operatorSettings           operatorSettingsStore
	engineController           *EngineController
	resolveSizingStore         func(context.Context, store.Team) (SizingStore, error)
	resolveCreditReader        func(context.Context, store.Team) (CreditReader, error)
	resolveConcurrencyStore    func(context.Context, store.Team) (ConcurrencyStore, error)
	resolveQueueReader         func(context.Context, store.Team) (QueueReader, error)
	resolveCredentialInventory func(context.Context, store.Team) (CredentialInventoryReader, error)
	resolveCredentialWriter    func(context.Context, store.Team) (CredentialWriter, error)
	resolveCredentialRotator   func(context.Context, store.Team) (CredentialRotator, error)
	resolveSourceAuditReader   func(context.Context, store.Team) (SourceAuditReader, error)
	resolveSecretReader        func(context.Context, store.Team) (SecretReader, error)
	resolveScheduleWriter      func(context.Context, store.Team) (ScheduleWriter, error)
	resolveBillingWriter       func(context.Context, store.Team) (BillingWriter, error)
	resolveTeamWriter          func(context.Context, store.Team) (TeamWriter, error)
	resolveGitHubConnectWriter func(context.Context, store.Team) (GitHubConnectWriter, error)
	resolveGitHubAppWriter     func(context.Context, store.Team) (GitHubAppWriter, error)
	resolveRunControlWriter    func(context.Context, store.Team) (RunControlWriter, error)

	peerIdentity func(*http.Request) capability.Subject

	loginLimit *loginLimiter

	flood                  *floodControl
	operatorSettingsWrites OperatorSettingsWriter
	resolveMetricWriter    func(context.Context, store.Team) (MetricWriter, error)
	requestBudget          *principalBudget
	tokenBudget            *tokenBudget

	idleClaimPoll     time.Duration
	idlePolls         *ratelimit.Limiter
	idlePollsEnforced bool
	lastClaimAward    atomic.Int64

	queueTimeout    time.Duration
	attentionCursor [2]string
	attentionPassAt time.Time

	sessionMaxLifetime time.Duration

	secretsCipher Cipher

	costPerRunnerHour float64
	costRateSource    string

	computeAlarmMu sync.Mutex
	computeAlarmOn bool

	bucketUsageStore storage.UsageReporter
	downloadStores   map[store.StorageKind]*teamblob.Store
	downloadS3       *s3.PresignClient
	downloadCDN      *sign.URLSigner
	downloadDomain   string
	directUploads    *directUploadS3
	logRetention     map[store.Team]int
	logLifecycle     logLifecycle
	egress           *egress.Meter

	dashboardURL string
	externalURL  string
	oidc         oidcState

	downloadFree, downloadFunded int64

	mailer             mailer.Mailer
	deletionHolderOnce sync.Once
	deletionHolderID   string

	metricsAddr      string
	trustedProxyAddr string
	metricsLn        net.Listener

	runnerHeadroom *runnerHeadroomRegistry

	liveLogs         *liveLogs
	runnerPresence   *runnerPresenceRegistry
	runnerHeartbeats *runnerHeartbeatRegistry
	placement        placementPolicy

	// safety: names this process in the cron tick lease and on each recorded
	// tick, so an operator can tell which controller evaluated.
	cronHolder  string
	leaseHolder string

	// safety: the cron evaluator's clock, so a test can resolve a due
	// instant without waiting a minute for one. Nil reads time.Now.
	cronNow func() time.Time

	routeProbeOnce sync.Once
	routeProbeMux  *http.ServeMux

	storage storageSample

	identity identityConfig

	tenants tenantCache

	githubRunners githubRunnerConfig

	githubApp       *githubAppState
	checkout        *billingCheckout
	paymentVerifier capability.PaymentAuthenticator
	paymentLedger   PaymentSettler
	operators       atomic.Pointer[map[string]bool]
	consoleWrites   ConsoleWriter

	dashboard     *Dashboard
	localHTTPOnce sync.Once
	localHTTP     wingd.LocalHTTP
}

// New constructs a Server bound to the given store. A trigger it accepts is
// recorded for a runner to claim. Callers own the store's lifecycle; New
// never closes it.
func New(st *store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	srv := &Server{
		store:              st,
		operatorSettings:   storeOperatorSettings{Store: st, Operator: st.AsOperator()},
		logger:             logger,
		loginLimit:         newLoginLimiter(),
		queueTimeout:       15 * time.Minute,
		sessionMaxLifetime: DefaultSessionMaxLifetime,
		runnerHeadroom:     newRunnerHeadroomRegistry(),
		liveLogs:           newLiveLogs(),
		runnerPresence:     newRunnerPresenceRegistry(),
		runnerHeartbeats:   newRunnerHeartbeatRegistry(),
		cronHolder:         defaultCronHolder(),
		leaseHolder:        rand.Text(),
		requestBudget:      newPrincipalBudget(RequestBudget{}),
		idleClaimPoll:      DefaultMaxIdleClaimPoll,
		downloadFree:       DefaultTeamDailyDownloadFreeBytes,
		downloadFunded:     DefaultTeamDailyDownloadFundedBytes,
	}
	if st != nil {
		srv.credentialIssuer = st
	}
	srv.authorization = srv.storeRouteAuthorization()
	srv.recordQueueActivity(time.Now())
	return srv
}

type placementPolicy struct {
	defaultPrefers []string
	hold           time.Duration
	liveness       time.Duration
}

// WithLocalFirstPlacement holds a node back from a claim-mode runner
// that does not advertise the node's Prefers, for hold measured from the node's
// ready time, while another runner that does advertise it has polled within
// liveness and has a free slot. After the hold any eligible runner takes the
// node. defaultPrefers applies to nodes whose plan declares no Prefers of their
// own; empty leaves those nodes to the first claimant. A hold of zero or less
// disables the rule, which is the first-in-first-out behavior of a controller
// that never calls this.
func (s *Server) WithLocalFirstPlacement(defaultPrefers []string, hold, liveness time.Duration) *Server {
	s.placement = placementPolicy{defaultPrefers: defaultPrefers, hold: hold, liveness: liveness}
	return s
}

// WithSessionMaxLifetime caps how long a browser session lives from the
// moment it was created. A session past the cap is refused and deleted
// instead of renewed, so a polling tab cannot keep one alive forever.
// Zero or less removes the cap and restores an unbounded sliding TTL.
func (s *Server) WithSessionMaxLifetime(d time.Duration) *Server {
	s.sessionMaxLifetime = d
	return s
}

// WithQueueTimeout overrides how long a trigger waits unclaimed before the
// reaper fails its run. Zero disables that sweep. A ready node waits for its
// claim wait instead, [match.DefaultClaimWait] unless its plan sets one.
func (s *Server) WithQueueTimeout(d time.Duration) *Server {
	s.queueTimeout = d
	return s
}

// WithCostRate sets the USD-per-runner-hour rate the receipt builder
// uses to compute compute_cents. source is echoed into the
// receipt's rate_source field for provenance. Zero rate = receipts
// report compute_cents:0, matching unconfigured-profile semantics.
func (s *Server) WithCostRate(rate float64, source string) *Server {
	s.costPerRunnerHour = rate
	s.costRateSource = source
	return s
}

// WithBucketUsage supplies read-only reporting for the bucket ceiling.
func (s *Server) WithBucketUsage(a storage.UsageReporter) *Server {
	s.bucketUsageStore = a
	return s
}

// WithDashboardURL announces the externally-reachable dashboard URL via
// GET /api/v1/services, so a client that has just been given a token can
// tell its operator where to watch the runs it dispatches. Empty disables
// the announcement.
func (s *Server) WithDashboardURL(url string) *Server {
	s.dashboardURL = url
	return s
}

// WithLiveLogLimits sizes the in-memory live log buffers: perNodeBytes
// per running node, totalBytes across every node together, maxNodes
// buffers at once, and idle before a node that stopped writing without
// finishing is released. A non-positive argument keeps that limit's
// default, so a caller that wants a value rejected rather than ignored
// validates it before calling; `sparkwing-controller` does.
func (s *Server) WithLiveLogLimits(perNodeBytes int, totalBytes int64, maxNodes int, idle time.Duration) *Server {
	if perNodeBytes > 0 {
		s.liveLogs.perNodeBytes = perNodeBytes
	}
	if totalBytes > 0 {
		s.liveLogs.totalBytes = totalBytes
	}
	if maxNodes > 0 {
		s.liveLogs.maxNodes = maxNodes
	}
	if idle > 0 {
		s.liveLogs.idle = idle
	}
	return s
}

// WithMetricsAddr moves the Prometheus endpoint off the main listener
// onto its own address, so /metrics is reachable only from wherever
// that address is bound and never through the public ingress. Empty
// keeps /metrics on the main listener.
func (s *Server) WithMetricsAddr(addr string) *Server {
	s.metricsAddr = addr
	return s
}

// WithMetricsListener moves the Prometheus endpoint onto a socket the
// caller already holds, and serves it there instead of binding the
// address [Server.WithMetricsAddr] takes. Pass a listener when the
// address is one the operating system assigned, so nothing else can take
// the port between the assignment and the bind.
//
// [ServeWith] closes the listener when it returns, so a Server configured
// this way serves once. A nil listener leaves the server on whatever
// [Server.WithMetricsAddr] gave it.
func (s *Server) WithMetricsListener(ln net.Listener) *Server {
	if ln == nil {
		return s
	}
	s.metricsLn = ln
	s.metricsAddr = ln.Addr().String()
	return s
}

// WithSecretsCipher configures row-bound credential encryption and session-key protection.
func (s *Server) WithSecretsCipher(c Cipher) *Server {
	s.secretsCipher = c
	if s.store != nil {
		var opener store.PlanSecretOpener
		if c != nil {
			opener = planSecretCipher{cipher: c}
		}
		s.store.SetPlanSecretOpener(opener)
		bound, _ := c.(store.SessionKeyCipher)
		s.store.SetSessionKeyCipher(bound)
	}
	return s
}

// WithTrustedProxyAddr serves the same API on a second address whose
// requests key login throttling, the bearer failure budget and the
// request log on their X-Real-IP. Only a proxy that overwrites that
// header may reach addr.
func (s *Server) WithTrustedProxyAddr(addr string) *Server {
	s.trustedProxyAddr = addr
	return s
}

// WithPeerIdentity installs the transport's verified Unix uid or Windows SID identity.
func (s *Server) WithPeerIdentity(fn func(*http.Request) capability.Subject) *Server {
	s.peerIdentity = fn
	return s
}

func (s *Server) identifyPeer(r *http.Request) (capability.Presentation, error) {
	if s.peerIdentity != nil {
		subject := s.peerIdentity(r)
		if subject.Kind == capability.SubjectLocal && subject.ID != "" {
			return capability.Presentation{Class: capability.CredentialPeer, Peer: subject}, nil
		}
	}
	return capability.Presentation{}, sessionUnavailable()
}

// Handler returns the HTTP router. Exposed separately from Serve so
// tests can wrap it in httptest without binding a real port. Callers using
// Handler directly must call Shutdown to drain server-owned background work.
// Engine and operator endpoints use RouteAuthorization. Health stays public.
func (s *Server) Handler() http.Handler {
	api := s.handler()
	if s.dashboard == nil {
		return api
	}
	return s.browserHandler(api)
}

func (s *Server) handler() http.Handler {
	router := s.routers()
	router.HandleFunc("/", s.writeRouteMiss)
	return s.observedHandler(router)
}

func (s *Server) observedHandler(router *http.ServeMux, labels ...*http.ServeMux) http.Handler {
	labels = append([]*http.ServeMux{router}, labels...)
	h := withStreamDeadlineControl(otelutil.WrapHandler("sparkwing-controller",
		withRequestLog(router, s.logger, muxRouteLabeler(labels...), func(r *http.Request) string {
			return ratelimit.ClientIP(r)
		})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Metering() {
			r = r.WithContext(store.WithoutCreditMetering(r.Context()))
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) routers() *http.ServeMux {
	router := http.NewServeMux()
	var endpoints []Endpoint
	endpoints = append(endpoints, s.operatorEndpoints()...)
	endpoints = append(endpoints, s.engineEndpoints()...)
	endpoints = append(endpoints, s.claimEndpoints()...)
	endpoints = append(endpoints, s.authorityEndpoints()...)
	endpoints = append(endpoints, s.capabilityEndpoints()...)
	endpoints = append(endpoints, s.credentialEndpoints()...)
	endpoints = append(endpoints, s.sizingEndpoints()...)
	endpoints = append(endpoints, s.creditEndpoints()...)
	endpoints = append(endpoints, s.queueEndpoints()...)
	endpoints = append(endpoints, s.concurrencyEndpoints()...)
	endpoints = append(endpoints, s.dataEndpoints()...)
	endpoints = append(endpoints, s.sourceEndpoints()...)
	endpoints = append(endpoints, s.actionsEndpoints()...)
	endpoints = append(endpoints, s.githubRunnerEndpoints()...)
	endpoints = append(endpoints, s.sessionEndpoints()...)
	endpoints = append(endpoints, s.accountEndpoints()...)
	endpoints = append(endpoints, s.reportingEndpoints()...)
	endpoints = append(endpoints, s.metricEndpoints()...)
	endpoints = append(endpoints, s.teamReadEndpoints()...)
	endpoints = append(endpoints, s.scheduleEndpoints()...)
	endpoints = append(endpoints, s.runControlEndpoints()...)
	endpoints = append(endpoints, s.teamWriteEndpoints()...)
	endpoints = append(endpoints, s.githubAppEndpoints()...)
	endpoints = append(endpoints, s.billingEndpoints()...)
	s.registerControllerEndpoints(router, endpoints)
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	if s.dataController != nil {
		if objects, ok := s.dataController.Objects.(*blob.Filesystem); ok {
			router.Handle("PUT /api/v1/data/uploads/{upload}", objects)
			router.Handle("GET /api/v1/data/objects/{key...}", objects)
			router.Handle("PUT /api/v1/log-segments/{key...}", objects)
		}
	}
	router.Handle("GET /api/v1/capabilities", http.HandlerFunc(s.handleCapabilities))
	router.Handle("POST /webhooks/github-app", http.HandlerFunc(s.handleGitHubAppWebhook))
	router.Handle("POST /webhooks/stripe", s.paymentHandler())

	return router
}

// safety: several routes are registered only when this server was given the
// surface behind them, so the answer is this server's, not the build's. It
// runs registration a second time rather than reading the serving muxes,
// because the caller asks before the store that server needs is open.
func (s *Server) routeProbe() *http.ServeMux {
	s.routeProbeOnce.Do(func() {
		s.routeProbeMux = s.routers()
	})
	return s.routeProbeMux
}

// RegisteredRoute reports whether this server registers a handler for the
// request's method and path. A caller that answers requests in front of a
// controller uses it to tell a route this server does not serve, which is a
// permanent answer, from a condition that may clear.
func (s *Server) RegisteredRoute(r *http.Request) bool {
	return routeRegistered(s.routeProbe(), r)
}

func (s *Server) writeRouteMiss(w http.ResponseWriter, r *http.Request) {
	writeRouteMiss(w, allowedRouteMethods(s.routeProbe(), r, routeProbeMethods))
}

// WriteUnsupportedRoute answers a request for a route this build does not
// register, in the body [UnsupportedRouteError] documents.
func WriteUnsupportedRoute(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": UnsupportedRouteError,
		"route": r.Method + " " + r.URL.Path,
	})
}

// UnsupportedRouteError is the `error` member of the 404 body a controller
// returns for a method and path it does not register. It names the surface
// rather than the resource, so a client can tell a route this controller
// predates from a row that does not exist and degrade instead of reporting
// the run missing.
const UnsupportedRouteError = "unsupported"

var routeProbeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// safety: a path registered for another method must keep answering 405 with
// its Allow header, which the mux only produces while no pattern matches, so
// the probe asks the mux rather than claiming the path is unserved.
func routeRegistered(mux *http.ServeMux, r *http.Request) bool {
	if _, pattern := mux.Handler(r); pattern != "" {
		return true
	}
	for _, method := range routeProbeMethods {
		if method == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := mux.Handler(probe); pattern != "" {
			return true
		}
	}
	return false
}

// safety: the HTTP drain and the commit-status drain each get this whole
// budget, so a request still folding a run's profiles is not cut off by
// time the listener's own shutdown already spent.
const controllerShutdownBudget = 5 * time.Second

// ServeWith runs a pre-built Server (configured with its With options)
// at addr. Split from Serve so the controller pod
// main can configure the server without passing options through Serve.
func ServeWith(ctx context.Context, s *Server, addr string) error {
	listener := func(addr string, h http.Handler) *http.Server {
		return &http.Server{
			Addr:              addr,
			Handler:           h,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
	}
	srv := listener(addr, s.Handler())
	servers := []*http.Server{srv}
	if s.trustedProxyAddr != "" {
		servers = append(servers, listener(s.trustedProxyAddr, ratelimit.TrustedListener(srv.Handler)))
	}

	s.reapLeased(ctx)

	s.loadEgressUsage(ctx)
	s.reconcileOwnedRuns(ctx)

	go s.runReaper(ctx, 10*time.Second)
	go s.runCreditSampler(ctx, creditSampleInterval)
	go s.runCronTick(ctx, cronTickOffer)
	go s.runStorageMaintenance(ctx, StorageMaintenanceInterval)
	go s.runBucketCeiling(ctx)
	go s.runTeamDeletions(ctx, TeamDeletionInterval)
	go s.runCardBilling(ctx, CardBillingInterval)

	errCh := make(chan error, 1+len(servers))

	metricsSrv := s.metricsServer()
	if metricsSrv != nil {
		metricsLn := s.metricsLn
		if metricsLn == nil {
			// safety: a metrics endpoint that never binds leaves the operator blind, so refuse to start instead.
			var err error
			metricsLn, err = net.Listen("tcp", metricsSrv.Addr)
			if err != nil {
				return fmt.Errorf("controller metrics listener: %w", err)
			}
		}
		go func() {
			s.logger.Info("controller metrics listening", "addr", metricsSrv.Addr)
			if err := metricsSrv.Serve(metricsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("controller metrics listener: %w", err)
			}
		}()
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := metricsSrv.Shutdown(shutdownCtx); err != nil {
				s.logger.Warn("controller metrics shutdown incomplete", "err", err)
			}
		}()
	}

	for _, l := range servers {
		go func() {
			s.logger.Info("controller listening", "addr", l.Addr, "trusted_proxy", l != srv)
			errCh <- l.ListenAndServe()
		}()
	}

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controllerShutdownBudget)
		defer cancel()
		for _, l := range servers {
			if err := l.Shutdown(shutdownCtx); err != nil {
				s.logger.Warn("controller HTTP shutdown incomplete", "addr", l.Addr, "err", err)
			}
		}
		s.drainGitHubChecks(ctx)
		return nil
	case err := <-errCh:
		s.drainGitHubChecks(ctx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// safety: the queue holds the terminal check run a finished run has no other
// producer for, so its drain owns a budget rather than inheriting whatever
// the listener's shutdown left of one.
func (s *Server) drainGitHubChecks(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), controllerShutdownBudget)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		s.logger.Warn("github check run shutdown incomplete", "err", err)
	}
}

// Shutdown drains server-owned background work until ctx expires. ServeWith
// calls Shutdown automatically; callers serving Handler directly must call it.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.githubApp == nil {
		return nil
	}
	return s.githubApp.checks.shutdown(ctx)
}

// WithExternalURL declares the externally reachable controller base URL.
func (s *Server) WithExternalURL(rawURL string) *Server {
	s.externalURL = strings.TrimRight(strings.TrimSpace(rawURL), "/")
	return s
}

func (s *Server) externalBaseURL(r *http.Request) string {
	base := s.externalURL
	if base == "" {
		base = requestBaseURL(r)
	}
	return base
}

// safety: a controller behind TLS termination sees a plain listener, so the
// forwarded scheme is the only thing that tells the caller's URL from the hop's.
func requestBaseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		if proto, _, _ := strings.Cut(forwarded, ","); proto != "" {
			scheme = strings.TrimSpace(proto)
		}
	}
	return scheme + "://" + r.Host
}

func (s *Server) runReaper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reapLeased(ctx)
			s.reconcileOwnedRuns(ctx)
			s.sampleOperationalSeries(ctx)
		}
	}
}

const reaperLeaseTTL = time.Minute

func (s *Server) reapLeased(ctx context.Context) bool {
	lease, err := s.store.AsOperator().AcquireSingletonLease(ctx, "controller.reaper", s.measureHolder(), reaperLeaseTTL)
	if errors.Is(err, store.ErrLockHeld) {
		return false
	}
	if err != nil {
		s.logger.Error("maintenance sweep lease failed", "err", err)
		return false
	}
	s.reap(ctx, lease)
	return true
}

func (s *Server) reap(ctx context.Context, lease store.SingletonLease) {
	concurrency, err := s.store.AsOperator().MaintainConcurrency(ctx, lease, store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		s.logger.Error("concurrency maintenance failed", "err", err)
	}
	for _, h := range concurrency.StaleHolders {
		s.logger.Warn("reaped stale concurrency holder",
			"key", h.Key, "holder_id", h.HolderID,
			"run_id", h.RunID, "node_id", h.NodeID)
	}
	for _, w := range concurrency.StaleWaiters {
		s.logger.Warn("reaped stale concurrency waiter",
			"key", w.Key, "run_id", w.RunID,
			"node_id", w.NodeID, "policy", w.Policy,
			"arrived_at", w.ArrivedAt.Format(time.RFC3339))
	}
	if recoveries, err := s.store.ReapUnits(ctx, lease, 500); err != nil {
		s.logger.Error("work recovery sweep failed", "err", err)
	} else {
		for _, recovery := range recoveries {
			s.logger.Info("recovered expired work",
				"run_id", recovery.RunID, "node_id", recovery.NodeID,
				"unit", recovery.Kind, "requeued", recovery.Requeued,
				"cancelled", recovery.Cancelled, "failed", recovery.Failed,
				"disruptions", recovery.Disruptions)
		}
	}
	s.sweepClaimAttention(ctx, time.Now(), claimAttentionBatch)
}

func (s *Server) sampleOperationalSeries(ctx context.Context) {
	if n, err := s.store.CountPendingNodes(ctx); err != nil {
		s.logger.Error("pending nodes sample failed", "err", err)
	} else {
		setPendingNodes(n)
	}
	if n, err := s.store.CountActiveRunners(ctx, runnerLivenessWindow); err != nil {
		s.logger.Error("active runners sample failed", "err", err)
	} else {
		setActiveRunners(n)
	}
	if counts, err := s.store.CountNodesByQueueState(ctx); err != nil {
		s.logger.Error("queue depth sample failed", "err", err)
	} else {
		setQueueDepth(counts)
	}
	liveRunners.sample(s.runnerPresence.liveLabelSets(time.Now(), runnerLivenessWindow))
}

// safety: both ledger sums scan a table that is never pruned, so they run on a
// timer of their own rather than on every reaper sweep.
func (s *Server) runCreditSampler(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		s.sampleCreditLedger(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) sampleCreditLedger(ctx context.Context) {
	if !s.Metering() {
		ledgerSnapshot.set(store.CreditLedgerTotals{})
		return
	}
	totals, err := s.store.CreditLedgerTotals(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("credit ledger sample failed", "err", err)
		}
		return
	}
	ledgerSnapshot.set(totals)
}

func withRequestLog(
	next http.Handler, logger *slog.Logger, routeLabel, clientIP func(*http.Request) string,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID(r)
		w.Header().Set(RequestIDHeader, id)
		ctx, rec := withAuditRecord(r.Context(), id, routeLabel(r))
		r = r.WithContext(ctx)
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		writer := http.ResponseWriter(rw)
		if _, ok := w.(http.Flusher); ok {
			writer = &flushingStatusRecorder{statusRecorder: rw}
		}
		start := time.Now()
		finish := func(status int) {
			elapsed := time.Since(start)
			observeHTTPRequest(rec.route, r.Method, status, elapsed)
			logRequest(ctx, logger, r, rec, status, elapsed, clientIP(r))
		}
		// safety: a handler that panics still made its write attempt, so it is
		// logged as a 500 before the panic continues to the server.
		defer func() {
			if p := recover(); p != nil {
				status := http.StatusInternalServerError
				if p == http.ErrAbortHandler { //nolint:errorlint // net/http compares the sentinel itself
					status = rw.status
				}
				finish(status)
				panic(p) //nolint:forbidigo // re-raise so net/http still sees the handler's panic
			}
		}()
		next.ServeHTTP(writer, r)
		finish(rw.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// safety: a stream sets its own write deadline through http.ResponseController, which reaches the connection only
// through Unwrap; without it the listener's WriteTimeout ends every stream.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

type flushingStatusRecorder struct {
	*statusRecorder
}

func (r *flushingStatusRecorder) Flush() {
	r.ResponseWriter.(http.Flusher).Flush()
}
