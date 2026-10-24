package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/crons"
	"github.com/sparkwing-dev/sparkwing/internal/deployment"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/routepolicy"
	"github.com/sparkwing-dev/sparkwing/internal/sessionledger"
	"github.com/sparkwing-dev/sparkwing/internal/storage"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// Server serves controller HTTP requests.
type Server struct {
	deployment           deployment.Config
	operator             *store.Operator
	operatorCapabilities CapabilityManager
	resolveTenant        func(context.Context, store.Team) (TenantHandle, error)
	logger               *slog.Logger

	hostKeyScan       func(ctx context.Context, host string, port int) (ssh.PublicKey, error)
	identityLinkLimit perMinuteLimiter

	credentialIssuer     CredentialIssuer
	runnerMeteringWriter RunnerMeteringWriter
	authorization        routepolicy.Authorization
	accountReads         AccountReader
	accountWrites        AccountWriter
	identityLinkWrites   IdentityLinkWriter
	dataController       *DataController
	sourceController     *SourceController
	operatorSettings     operatorSettingsStore
	engineController     *EngineController

	peerIdentity func(*http.Request) capability.Subject

	loginLimit *loginLimiter

	flood                  *floodControl
	operatorSettingsWrites OperatorSettingsWriter
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
	logRetention     map[store.Team]int
	egress           *egress.Meter

	dashboardURL string
	oidc         oidcState

	downloadFree, downloadFunded int64

	mailer             mailer.Mailer
	deletionHolderOnce sync.Once
	deletionHolderID   string

	metricsAddr       string
	deliverySpools    chan struct{}
	trustedProxyCIDRs []netip.Prefix

	liveLogs  *liveLogs
	placement placementPolicy

	// safety: names this process in the cron tick lease and on each recorded
	// tick, so an operator can tell which controller evaluated.
	cronHolder  string
	leaseHolder string

	// safety: the cron evaluator's clock, so a test can resolve a due
	// instant without waiting a minute for one. Nil reads time.Now.
	cronNow func() time.Time

	routesMu      sync.Mutex
	servingRoutes *registeredMux

	storage storageSample

	identity identityConfig

	githubRunners githubRunnerConfig

	githubApp         *githubAppState
	githubAppProblem  error
	checkout          *billingCheckout
	paymentDeployment string
	paymentVerifier   capability.PaymentAuthenticator
	paymentLedger     PaymentSettler
	operators         atomic.Pointer[map[string]bool]
	consoleWrites     consoleWriteTransactions

	dashboard                 *Dashboard
	localHTTPOnce             sync.Once
	localHTTP                 localhostwire.LocalHTTP
	localSessionMu            sync.Mutex
	localSessions             *sessionledger.Ledger
	localSchedules            *crons.Service
	localScheduleProver       LocalScheduleProver
	localScheduleEnvironments LocalScheduleEnvironments
}

type placementPolicy struct {
	defaultPrefers []string
	hold           time.Duration
	liveness       time.Duration
}

// WithLocalFirstPlacement records the controller placement settings.
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

// WithQueueTimeout sets the queue timeout.
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

// WithSecretsCipher configures row-bound credential encryption and session-key protection.
func (s *Server) WithSecretsCipher(c Cipher) *Server {
	s.secretsCipher = c
	return s
}

// WithTrustedProxyCIDRs configures proxy networks whose forwarded client addresses are trusted.
func (s *Server) WithTrustedProxyCIDRs(prefixes []netip.Prefix) *Server {
	s.trustedProxyCIDRs = slices.Clone(prefixes)
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

// Handler returns the HTTP router; callers must call Shutdown to drain server-owned background work.
func (s *Server) Handler() http.Handler {
	var handler http.Handler = s.handler()
	if s.dashboard != nil {
		handler = s.browserHandler(handler)
	}
	return ratelimit.TrustedProxies(handler, s.trustedProxyCIDRs)
}

func (s *Server) handler() http.Handler {
	router := s.rebuildRouters()
	next := s.observedHandler(router.ServeMux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(s.placementContext(r.Context())))
	})
}

func (s *Server) observedHandler(router *http.ServeMux, labels ...*http.ServeMux) http.Handler {
	labels = append([]*http.ServeMux{router}, labels...)
	h := withStreamDeadlineControl(otelutil.WrapHandler("sparkwing-controller",
		withRequestLog(router, s.logger, muxRouteLabeler(labels...), func(r *http.Request) string {
			return ratelimit.ClientIP(r)
		})))
	return h
}

func (s *Server) routers() *http.ServeMux {
	return s.registeredRouters().ServeMux
}

func (s *Server) registeredRouters() *registeredMux {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	if s.servingRoutes == nil {
		s.servingRoutes = s.buildRegisteredRouters()
	}
	return s.servingRoutes
}

func (s *Server) rebuildRouters() *registeredMux {
	s.routesMu.Lock()
	defer s.routesMu.Unlock()
	s.servingRoutes = s.buildRegisteredRouters()
	return s.servingRoutes
}

func (s *Server) buildRegisteredRouters() *registeredMux {
	router := newRegisteredMux()
	var endpoints []controllerwire.Endpoint
	endpoints = append(endpoints, s.operatorEndpoints()...)
	endpoints = append(endpoints, s.engineEndpoints()...)
	endpoints = append(endpoints, s.localHostEndpoints()...)
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
	endpoints = append(endpoints, s.sourceUploadEndpoints()...)
	endpoints = append(endpoints, s.actionsEndpoints()...)
	endpoints = append(endpoints, s.githubRunnerEndpoints()...)
	endpoints = append(endpoints, s.sessionEndpoints()...)
	endpoints = append(endpoints, s.accountEndpoints()...)
	endpoints = append(endpoints, s.reportingEndpoints()...)
	endpoints = append(endpoints, s.metricEndpoints()...)
	endpoints = append(endpoints, s.teamReadEndpoints()...)
	endpoints = append(endpoints, s.scheduleEndpoints()...)
	endpoints = append(endpoints, s.runControlEndpoints()...)
	endpoints = append(endpoints, s.missingOperationEndpoints()...)
	endpoints = append(endpoints, s.teamWriteEndpoints()...)
	endpoints = append(endpoints, s.githubAppEndpoints()...)
	endpoints = append(endpoints, s.billingEndpoints()...)
	s.registerControllerEndpoints(router, endpoints)
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	if s.dataController != nil {
		if objects, ok := s.dataController.Objects.(*blob.Filesystem); ok {
			router.Handle("PUT /api/v1/data/uploads/{upload}", objects)
			router.HandleFunc("GET /api/v1/data/objects/{key...}", func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					w.Header().Set("Allow", http.MethodGet)
					routepolicy.WritePolicyError(w, &apierr.Error{Code: apierr.MethodNotAllowed, Message: "method is not registered"})
					return
				}
				objects.ServeHTTP(w, r)
			})
			router.Handle("PUT /api/v1/log-segments/{key...}", objects)
		}
	}
	router.Handle("GET /api/v1/capabilities", http.HandlerFunc(s.handleCapabilities))
	router.Handle("POST /webhooks/github-app", http.HandlerFunc(s.handleGitHubAppWebhook))
	router.Handle("POST /webhooks/stripe", s.paymentHandler())

	router.HandleFunc("/", s.writeRouteMiss)
	return router
}

// Registrations returns the method and pattern pairs installed by this server's serving routers.
func (s *Server) Registrations() []string {
	routes := s.registeredRouters().registrations
	if s.dashboard != nil && !s.dashboard.Local {
		browser := newRegisteredMux()
		s.signInRoutes(browser)
		routes = append(routes, browser.registrations...)
	}
	if metrics := s.metricsServer(); metrics != nil {
		routes = append(routes, metrics.Handler.(*registeredMux).registrations...)
	}
	slices.Sort(routes)
	return routes
}

// RegisteredRoute reports whether this server registers a handler for the
// request's method and path. A caller that answers requests in front of a
// controller uses it to tell a route this server does not serve, which is a
// permanent answer, from a condition that may clear.
func (s *Server) RegisteredRoute(r *http.Request) bool {
	return routeRegistered(s.registeredRouters().ServeMux, r)
}

func (s *Server) writeRouteMiss(w http.ResponseWriter, r *http.Request) {
	routepolicy.WriteRouteMiss(w, routepolicy.AllowedRouteMethods(s.registeredRouters().ServeMux, r, routeProbeMethods))
}

var routeProbeMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

// safety: a path registered for another method must keep answering 405 with
// its Allow header, which the mux only produces while no pattern matches, so
// the probe asks the mux rather than claiming the path is unserved.
func routeRegistered(mux *http.ServeMux, r *http.Request) bool {
	if _, pattern := mux.Handler(r); strings.Contains(pattern, " ") {
		return true
	}
	for _, method := range routeProbeMethods {
		if method == r.Method {
			continue
		}
		probe := r.Clone(r.Context())
		probe.Method = method
		if _, pattern := mux.Handler(probe); strings.Contains(pattern, " ") {
			return true
		}
	}
	return false
}

// safety: the HTTP drain and the commit-status drain each get this whole
// budget, so a request still folding a run's profiles is not cut off by
// time the listener's own shutdown already spent.
const controllerShutdownBudget = 5 * time.Second

// Listeners are the sockets a Server serves on, bound by the caller before
// startup changes any stored state, so a port that cannot be had fails the
// start without side effects.
type Listeners struct {
	// API serves the controller.
	API net.Listener
	// TrustedProxy serves the same API keyed on X-Real-IP; nil serves none.
	TrustedProxy net.Listener
	// Metrics serves /metrics when [Server.WithMetricsAddr] moved it off API.
	Metrics net.Listener
}

// ServeWith runs a Server configured through its With options on ln,
// refusing one whose engine has no runner admission policy. Once serving
// starts, it closes every listener when it returns; a refusal before then
// leaves them to the caller.
func ServeWith(ctx context.Context, s *Server, ln Listeners) error {
	metricsSrv := s.metricsServer()
	switch {
	case s.engineController.RunnerPolicy == nil:
		return errors.New("controller has no runner admission policy; compose its engine with WithEngineController before serving")
	case ln.API == nil:
		return errors.New("controller has no API listener")
	case metricsSrv != nil && ln.Metrics == nil:
		return errors.New("controller metrics address " + s.metricsAddr + " has no listener")
	}
	ctx, stopWorkers := context.WithCancel(ctx)
	defer stopWorkers()
	listener := func(h http.Handler) *http.Server {
		return &http.Server{
			Handler:           h,
			ErrorLog:          slog.NewLogLogger(s.logger.Handler(), slog.LevelError),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       2 * time.Minute,
		}
	}
	type served struct {
		srv *http.Server
		ln  net.Listener
	}
	api := listener(s.Handler())
	servers := []served{{api, ln.API}}
	if ln.TrustedProxy != nil {
		servers = append(servers, served{listener(ratelimit.TrustedListener(api.Handler)), ln.TrustedProxy})
	}
	if metricsSrv != nil {
		servers = append(servers, served{metricsSrv, ln.Metrics})
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
	go s.runBilling(ctx, BillingInterval)
	go s.runLicenseAlarm(ctx, licenseAlarmInterval)
	inboxDone := make(chan struct{})
	go func() {
		defer close(inboxDone)
		s.runGitHubInbox(ctx)
	}()
	defer func() {
		stopWorkers()
		<-inboxDone
	}()

	errCh := make(chan error, len(servers))
	for _, l := range servers {
		go func() {
			s.logger.Info("controller listening", "addr", l.ln.Addr().String())
			if err := l.srv.Serve(l.ln); !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("controller listener %s: %w", l.ln.Addr(), err)
			}
		}()
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-errCh:
	}
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controllerShutdownBudget)
	defer cancel()
	for _, l := range servers {
		if serr := l.srv.Shutdown(shutdownCtx); serr != nil {
			s.logger.Warn("controller HTTP shutdown incomplete", "addr", l.ln.Addr().String(), "err", serr)
		}
	}
	return err
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
			if err := s.ReconcileRunEffects(ctx); err != nil && ctx.Err() == nil {
				s.logger.Error("run completion failed", "err", err)
			}
			s.sampleOperationalSeries(ctx)
		}
	}
}

const reaperLeaseTTL = time.Minute

const licenseAlarmInterval = time.Hour

// safety: an expired license leaves the deployment running as declared, so
// the operator learns of it from this error and from health, never from
// metering or sign-in quietly switching off.
func (s *Server) runLicenseAlarm(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if problem := s.deployment.LicenseProblem(time.Now()); problem != "" {
			s.logger.Error("deployment license is no longer valid", "kind", s.deployment.Kind(), "problem", problem)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) reapLeased(ctx context.Context) bool {
	lease, err := s.operator.AcquireSingletonLease(ctx, "controller.reaper", s.leaseHolder, reaperLeaseTTL)
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
	concurrency, err := s.operator.MaintainConcurrency(ctx, lease)
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
	if recoveries, err := s.operator.ReapUnits(ctx, lease, 500); err != nil {
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
	if counts, err := s.operator.CountNodesByQueueState(ctx); err != nil {
		s.logger.Error("queue depth sample failed", "err", err)
	} else {
		setPendingNodes(counts[store.QueueStateReady])
		setQueueDepth(counts)
	}
	s.sampleRunners(ctx)
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
	if !s.deployment.Metering() {
		ledgerSnapshot.set(store.CreditLedgerTotals{})
		return
	}
	totals, err := s.operator.CreditLedgerTotals(ctx)
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
	logger = slog.New(CredentialSafeLogHandler(logger.Handler()))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := requestID()
		w.Header().Set(RequestIDHeader, id)
		ctx, masker := withRequestMasker(r.Context())
		ctx, rec := withAuditRecord(ctx, id, routeLabel(r))
		r = r.WithContext(ctx)
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		writer := http.ResponseWriter(rw)
		if _, ok := w.(http.Flusher); ok {
			writer = &flushingStatusRecorder{statusRecorder: rw}
		}
		start := time.Now()
		finish := func(status int) {
			elapsed := time.Since(start)
			if status == http.StatusNotFound && rec.route == otherLabel {
				rec.path = sanitizedMissingPath(r.URL.EscapedPath(), masker.Mask)
			}
			if status >= http.StatusInternalServerError {
				cause := rw.cause
				if cause == nil {
					cause = errors.New(http.StatusText(status))
				}
				logger.ErrorContext(ctx, "http request failed", "request_id", masker.Mask(rec.id), "method", r.Method, "route", rec.route, "status", status, "err", masker.Mask(cause.Error()))
			}
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
				if status >= http.StatusInternalServerError {
					rw.cause = fmt.Errorf("handler panic: %v", p)
				}
				finish(status)
				panic(http.ErrAbortHandler) //nolint:forbidigo // net/http closes the connection without logging the unmasked panic
			}
		}()
		next.ServeHTTP(writer, r)
		finish(rw.status)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	cause  error
}

func (r *statusRecorder) ResponseError(err error) {
	if r.cause == nil {
		r.cause = err
	}
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

func (s *Server) placementContext(ctx context.Context) context.Context {
	return store.WithClaimPlacement(ctx, store.ClaimPlacement{Preferences: []store.PlacementPreference{{Prefers: s.placement.defaultPrefers, Hold: s.placement.hold}, {}}, Liveness: s.placement.liveness})
}
