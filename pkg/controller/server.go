// Package controller is the cluster-mode control plane: an HTTP
// service fronting the run/node/event/cache state store. The
// controller is the single writer; short-lived orchestrator pods talk
// to it for every state transition and use the same interface as
// local-mode callers (orchestrator.StateBackend).
//
// The matching HTTP client (StateBackend against a remote controller)
// lives in pkg/controller/client.
package controller

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
	"github.com/sparkwing-dev/sparkwing/secrets"
)

// Server owns the route table, the backing store, and the
// dispatcher. A single Server instance services all concurrent HTTP
// requests; the store itself serializes writes.
type Server struct {
	store      *store.Store
	dispatcher Dispatcher
	logger     *slog.Logger
	// pool is the optional warm-PVC pool binding. Nil when the
	// controller runs without K8s API access; see AttachPool.
	pool *poolBinding
	// auth wraps every authenticated request and stamps a Principal on
	// ctx. Nil = auth fully disabled (laptop-local dev).
	auth *Authenticator
	// githubWebhookSecret verifies HMAC signatures on /webhooks/github
	// deliveries. Empty = endpoint returns 503.
	githubWebhookSecret string
	// queueTimeout is how long a node may sit with ready_at set and
	// claimed_by NULL before the reaper terminates it with
	// failure_reason=queue_timeout. Zero disables the sweep.
	queueTimeout time.Duration
	// concurrencyCacheCap bounds the total rows retained in the
	// concurrency_cache table. Zero disables LRU eviction (TTL still
	// applies). Default 10_000.
	concurrencyCacheCap int

	// secretsCipher, when non-nil, encrypts secret values at rest. Nil
	// means the controller runs unencrypted (laptop dev).
	secretsCipher *secrets.Cipher

	// costPerRunnerHour is the USD rate fed into receipt cost
	// computation. Zero = unconfigured -> compute_cents=0
	// in receipts. costRateSource is the human-readable provenance
	// string the receipt echoes back (e.g. "controller config").
	costPerRunnerHour float64
	costRateSource    string

	// bootstrap* caches the users-table-empty check for the
	// unauthenticated /api/v1/auth/bootstrap-needed probe. Cache is
	// one-way: once the table becomes non-empty, the "false" answer is
	// latched and we never probe the store again.
	bootstrapMu     sync.Mutex
	bootstrapExpiry time.Time
	bootstrapNeeded bool
	bootstrapClosed bool
}

// New constructs a Server bound to the given store. A nil dispatcher
// defaults to NoopDispatcher (triggers are recorded but no run is
// launched). Callers own the store's lifecycle; New never closes it.
func New(st *store.Store, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store:               st,
		dispatcher:          NoopDispatcher{Logger: logger},
		logger:              logger,
		queueTimeout:        15 * time.Minute,
		concurrencyCacheCap: 10_000,
	}
}

// WithQueueTimeout overrides the default queue-timeout window used by
// the reaper sweep. Zero disables the sweep entirely.
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

// WithSecretsCipher binds the controller's secret encryption-at-rest
// cipher. Reads are no-ops for rows that predate the cipher. Pass nil
// to keep the controller running unencrypted.
func (s *Server) WithSecretsCipher(c *secrets.Cipher) *Server {
	s.secretsCipher = c
	return s
}

// bootstrapAllowed reports whether the first-visit signup path is
// currently live (users table is empty). Result is cached for 60s.
// Once observed-as-non-empty, the answer is latched false until a
// process restart.
func (s *Server) bootstrapAllowed() bool {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	if s.bootstrapClosed {
		return false
	}
	if !s.bootstrapExpiry.IsZero() && time.Now().Before(s.bootstrapExpiry) {
		return s.bootstrapNeeded
	}
	needed := true
	if s.store != nil {
		n, err := s.store.CountUsers()
		if err == nil && n > 0 {
			needed = false
		}
	}
	s.bootstrapNeeded = needed
	s.bootstrapExpiry = time.Now().Add(60 * time.Second)
	if !needed {
		s.bootstrapClosed = true
	}
	return needed
}

// markBootstrapClosed latches the bootstrap path shut so the probe
// immediately returns false instead of waiting out the 60s cache.
func (s *Server) markBootstrapClosed() {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	s.bootstrapClosed = true
	s.bootstrapNeeded = false
	s.bootstrapExpiry = time.Now().Add(60 * time.Second)
}

// authMiddleware returns a non-nil Authenticator (a sentinel disabled
// one if none was configured) so Middleware's branch logic stays
// centralized.
func (s *Server) authMiddleware() *Authenticator {
	if s.auth != nil {
		return s.auth
	}
	return &Authenticator{
		now: func() time.Time { return time.Now().UTC() },
	}
}

// WithDispatcher returns a Server that invokes the given dispatcher
// when a trigger arrives. Separate from New so the dispatcher can
// close over the Server itself.
func (s *Server) WithDispatcher(d Dispatcher) *Server {
	s.dispatcher = d
	return s
}

// EnableAuthFromStore wires the Authenticator against the server's
// tokens table IF the table has any non-revoked rows. Empty table =
// auth stays disabled (pass-through).
//
// The tokens-table check happens ONCE at startup: a fresh row added
// via POST /api/v1/tokens takes effect on the next controller restart.
func (s *Server) EnableAuthFromStore() *Server {
	if !s.tokensTableNonEmpty() {
		s.auth = nil
		return s
	}
	s.auth = NewAuthenticator(s.store, 60*time.Second)
	return s
}

// tokensTableNonEmpty reports whether the tokens table has any
// non-revoked rows at startup.
func (s *Server) tokensTableNonEmpty() bool {
	if s.store == nil {
		return false
	}
	toks, err := s.store.ListTokens("", false)
	if err != nil {
		return false
	}
	return len(toks) > 0
}

// WithAuthenticator installs a pre-built Authenticator.
func (s *Server) WithAuthenticator(a *Authenticator) *Server {
	s.auth = a
	return s
}

// Handler returns the HTTP router. Exposed separately from Serve so
// tests can wrap it in httptest without binding a real port.
//
// Auth shape:
//   - /api/v1/health is always unauthenticated so k8s probes don't
//     401-crashloop the pod.
//   - Everything else goes through Authenticator.Middleware which
//     stamps a Principal on ctx (or 401s). Handlers declare scope via
//     requireScope.
//   - When the Authenticator is disabled, middleware + requireScope are
//     pass-through.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Runs: lifecycle writes + read surface for dashboards/CLI.
	mux.Handle("POST /api/v1/runs", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateRun)))
	mux.Handle("GET /api/v1/runs", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListRuns)))
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetRun)))
	mux.Handle("GET /api/v1/runs/{id}/nodes", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodes)))
	// per-run audit + cost receipt; recomputed on demand.
	mux.Handle("GET /api/v1/runs/{id}/receipt", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetRunReceipt)))
	mux.Handle("POST /api/v1/runs/{id}/finish", requireScope(ScopeAdmin, http.HandlerFunc(s.handleFinishRun)))
	mux.Handle("POST /api/v1/runs/{id}/plan", requireScope(ScopeAdmin, http.HandlerFunc(s.handleUpdatePlanSnapshot)))

	// Nodes: lifecycle writes for individual DAG nodes. Workers
	// (orchestrator) call these, so they need admin scope.
	mux.Handle("POST /api/v1/runs/{id}/nodes", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateNode)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/start", requireScope(ScopeAdmin, http.HandlerFunc(s.handleStartNode)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/finish", requireScope(ScopeAdmin, http.HandlerFunc(s.handleFinishNode)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/deps", requireScope(ScopeAdmin, http.HandlerFunc(s.handleUpdateNodeDeps)))
	// nodes.claim scope: the same runner that claims a node reads its
	// upstream refs.
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleGetNode)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/output", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleGetNodeOutput)))
	// Dispatch snapshots: runners write at dispatch time; dashboard reads.
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/dispatch", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleWriteNodeDispatch)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetNodeDispatch)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/dispatches", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodeDispatches)))

	// Events: append-only ordered log per run.
	mux.Handle("POST /api/v1/runs/{id}/events", requireScope(ScopeAdmin, http.HandlerFunc(s.handleAppendEvent)))

	// Triggers.
	mux.Handle("POST /api/v1/triggers", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleTrigger)))
	mux.Handle("POST /api/v1/triggers/claim", requireScope(ScopeAdmin, http.HandlerFunc(s.handleClaimTrigger)))
	mux.Handle("POST /api/v1/triggers/{id}/heartbeat", requireScope(ScopeAdmin, http.HandlerFunc(s.handleHeartbeat)))
	mux.Handle("POST /api/v1/triggers/{id}/done", requireScope(ScopeAdmin, http.HandlerFunc(s.handleFinishTrigger)))
	mux.Handle("GET /api/v1/triggers", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleListTriggers)))
	// Static-segment path so the {id} matcher below doesn't consume
	// "spawned-child" as an id.
	mux.Handle("GET /api/v1/triggers/spawned-child", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleFindSpawnedChildTrigger)))
	mux.Handle("GET /api/v1/triggers/{id}", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleGetTrigger)))

	// Operator cancellation.
	mux.Handle("POST /api/v1/runs/{id}/cancel", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleCancelRun)))

	// Read-side aggregations.
	mux.Handle("GET /api/v1/trends", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleTrends)))
	mux.Handle("GET /api/v1/agents", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleAgents)))

	// Retry: creates a fresh run. Same write scope as triggers.
	mux.Handle("POST /api/v1/runs/{id}/retry", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleRetry)))

	// Cross-pipeline refs: "latest run of pipeline X matching these
	// statuses / within this age." Powers sparkwing.Ref[T].Get.
	mux.Handle("GET /api/v1/pipelines/{name}/latest", requireScope(ScopeRunsRead, http.HandlerFunc(s.handlePipelineLatest)))

	// Per-node metrics.
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/metrics", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleAddNodeMetric)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/metrics", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetNodeMetrics)))

	// Maintenance.
	mux.Handle("DELETE /api/v1/runs/{id}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteRun)))

	// Concurrency primitive: supports all 5 OnLimit policies plus
	// optional memoization.
	mux.Handle("POST /api/v1/concurrency/{key}/acquire", requireScope(ScopeAdmin, http.HandlerFunc(s.handleAcquireSlot)))
	mux.Handle("POST /api/v1/concurrency/{key}/heartbeat", requireScope(ScopeAdmin, http.HandlerFunc(s.handleHeartbeatSlot)))
	mux.Handle("POST /api/v1/concurrency/{key}/release", requireScope(ScopeAdmin, http.HandlerFunc(s.handleReleaseSlot)))
	mux.Handle("GET /api/v1/concurrency/{key}/state", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleConcurrencyState)))
	mux.Handle("GET /api/v1/concurrency/{key}/notify", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleWaiterNotify)))

	// Node claim surface.
	mux.Handle("POST /api/v1/nodes/claim", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleClaimNode)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleMarkNodeReady)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/revoke-ready", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleRevokeNodeReady)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleHeartbeatNodeClaim)))

	// Activity / heartbeat surface for the dashboard's liveness dot.
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/activity", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleUpdateNodeActivity)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/touch", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleTouchNodeHeartbeat)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/annotations", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleAppendNodeAnnotation)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/summary", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleSetNodeSummary)))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleStartNodeStep)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleFinishNodeStep)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleSkipNodeStep)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleAppendStepAnnotation)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleSetStepSummary)))
	mux.Handle("GET /api/v1/runs/{id}/steps", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodeSteps)))

	// Debug pauses. /paused is an alias for the dashboard SPA;
	// /debug-pauses is the orchestrator + admin-write surface.
	mux.Handle("POST /api/v1/runs/{id}/debug-pauses", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateDebugPause)))
	mux.Handle("GET /api/v1/runs/{id}/debug-pauses", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListDebugPauses)))
	mux.Handle("GET /api/v1/runs/{id}/paused", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListDebugPauses)))

	// Event log tail (structured SSE). Dashboard SSE endpoint lives on
	// the web server; this is the underlying read.
	mux.Handle("GET /api/v1/runs/{id}/events", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListEvents)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetActiveDebugPause)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/release", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleReleaseDebugPause)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/status", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetNodeStatus)))

	// Approval gates. Request is orchestrator-written (admin), resolve
	// is human-facing (approvals.write), reads open via approvals.read.
	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}/request", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRequestApproval)))
	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeApprovalsWrite, http.HandlerFunc(s.handleResolveApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListApprovalsForRun)))
	mux.Handle("GET /api/v1/approvals/pending", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListPendingApprovals)))

	// Warm-PVC pool.
	mux.Handle("GET /api/v1/pool", requireScope(ScopeRunsRead, http.HandlerFunc(s.handlePoolList)))
	mux.Handle("POST /api/v1/pool/checkout", requireScope(ScopeAdmin, http.HandlerFunc(s.handlePoolCheckout)))
	mux.Handle("POST /api/v1/pool/return", requireScope(ScopeAdmin, http.HandlerFunc(s.handlePoolReturn)))
	mux.Handle("POST /api/v1/pool/heartbeat", requireScope(ScopeAdmin, http.HandlerFunc(s.handlePoolHeartbeat)))

	// Tokens CRUD. Admin-only; the bootstrap admin token is minted
	// out-of-band via `sparkwing tokens create`.
	mux.Handle("POST /api/v1/tokens", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateToken)))
	mux.Handle("GET /api/v1/tokens", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListTokens)))
	mux.Handle("GET /api/v1/tokens/{prefix}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleLookupTokenByPrefix)))
	mux.Handle("DELETE /api/v1/tokens/{prefix}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRevokeToken)))

	// Auth introspection: returns the calling principal + scopes for
	// whichever token authenticated the request.
	mux.Handle("GET /api/v1/auth/whoami", http.HandlerFunc(s.handleWhoami))

	// Session lookup: web pod resolves the sw_session cookie by passing
	// it as `Authorization: Session <raw>`. The caller presenting the
	// session id is the same trust the cookie grants.
	mux.Handle("GET /api/v1/auth/session", http.HandlerFunc(s.handleSession))

	// Token rotation.
	mux.Handle("POST /api/v1/tokens/{prefix}/rotate", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRotateToken)))

	// Users CRUD. POST /api/v1/users is registered on the OUTER router
	// instead so the first-visit signup path can accept an
	// unauthenticated first-admin create when the table is empty.
	mux.Handle("GET /api/v1/users", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListUsers)))
	mux.Handle("DELETE /api/v1/users/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteUser)))

	// Secrets CRUD. Admin-only because GET returns the raw value.
	mux.Handle("POST /api/v1/secrets", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateSecret)))
	mux.Handle("GET /api/v1/secrets", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListSecrets)))
	mux.Handle("GET /api/v1/secrets/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleGetSecret)))
	mux.Handle("DELETE /api/v1/secrets/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteSecret)))

	// Health + login + session + bootstrap probe + metrics + webhook
	// route at the outermost layer so they never see an Authorization
	// check.
	authed := s.authMiddleware().Middleware(mux)

	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	router.Handle("POST /api/v1/auth/login", http.HandlerFunc(s.handleLogin))
	router.Handle("POST /api/v1/auth/logout", http.HandlerFunc(s.handleLogout))
	router.Handle("GET /api/v1/auth/session", http.HandlerFunc(s.handleSession))
	router.Handle("GET /api/v1/auth/bootstrap-needed", http.HandlerFunc(s.handleBootstrapNeeded))
	// POST /api/v1/users routes through the outer router so the
	// handler can choose "unauthenticated bootstrap" vs "admin-scoped
	// create" on its own. See handleCreateUserOrBootstrap.
	router.Handle("POST /api/v1/users", http.HandlerFunc(s.handleCreateUserOrBootstrap))
	router.Handle("GET /metrics", metricsHandler())
	// GitHub webhook intake. HMAC-verified inside the handler; bearer
	// auth does not apply because GitHub cannot carry one.
	router.Handle("POST /webhooks/github/{pipeline}", http.HandlerFunc(s.handleGitHubWebhook))
	router.Handle("/", authed)

	// otelhttp wraps the outermost layer; withRequestLog stays inside
	// so log lines carry the trace_id via otelutil's slog bridge.
	return otelutil.WrapHandler("sparkwing-controller", withRequestLog(router, s.logger))
}

// Serve starts the HTTP listener and blocks until ctx is done. On
// ctx cancellation the server gracefully drains in-flight requests
// up to shutdownTimeout. Also spawns the reaper goroutine that
// re-queues triggers whose runner lease expired, and -- when a pool
// has been attached via Server.AttachPool -- the pool's reconcile
// and warming loops.
func Serve(ctx context.Context, st *store.Store, addr string, logger *slog.Logger) error {
	return ServeWith(ctx, New(st, logger), addr)
}

// ServeWith runs a pre-built Server (configured with WithDispatcher /
// AttachPool) at addr. Split from Serve so the controller pod main can
// wire in an in-cluster k8s client without passing options through
// Serve.
func ServeWith(ctx context.Context, s *Server, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	// If the previous controller crashed between a release commit and
	// the matching PromoteNextWaiters tx, keys can have queued waiters
	// and open capacity sitting idle. Sweep once on startup so those
	// waiters don't wait for a new arrival to unstick them.
	if n, err := s.store.ReconcileConcurrencyKeys(ctx, store.DefaultConcurrencyLease); err != nil {
		s.logger.Warn("concurrency reconcile on startup failed", "err", err)
	} else if n > 0 {
		s.logger.Info("concurrency reconcile promoted stranded waiters", "count", n)
	}

	go s.runReaper(ctx, 10*time.Second)

	// Pool loops run only when AttachPool was called. Without it, the
	// pool HTTP handlers return 503 until the loops report ready.
	if s.pool != nil {
		go s.pool.run(ctx, s.logger)
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Info("controller listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// runReaper is the crash-recovery sweep. Every `interval` it
// re-queues triggers whose lease has expired and cascade-fails the
// associated run + nodes so the dashboard reflects the real state.
func (s *Server) runReaper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Stale holder sweep promotes the next FIFO waiter so a
			// crashed pod mid-node doesn't wedge the whole key. Cache
			// TTL and LRU sweeps keep the cache table bounded.
			if stale, err := s.store.ReapStaleConcurrencyHolders(ctx); err != nil {
				s.logger.Error("concurrency stale-holder reap failed", "err", err)
			} else {
				for _, h := range stale {
					s.logger.Warn("reaped stale concurrency holder",
						"key", h.Key, "holder_id", h.HolderID,
						"run_id", h.RunID, "node_id", h.NodeID)
					if _, err := s.store.PromoteNextWaiters(ctx, h.Key, store.DefaultConcurrencyLease); err != nil {
						s.logger.Error("promote after stale-holder reap failed",
							"key", h.Key, "err", err)
					}
				}
			}
			if n, err := s.store.SweepExpiredConcurrencyCache(ctx); err != nil {
				s.logger.Error("concurrency cache TTL sweep failed", "err", err)
			} else if n > 0 {
				s.logger.Info("swept expired concurrency cache entries", "count", n)
			}
			// Orphan coalesce followers (leader gone) and any waiter
			// older than 2x the node lease, lining up with the
			// node-level queue timeout.
			if dropped, err := s.store.ReapStaleConcurrencyWaiters(ctx, 2*store.DefaultConcurrencyLease); err != nil {
				s.logger.Error("concurrency waiter reap failed", "err", err)
			} else {
				for _, w := range dropped {
					s.logger.Warn("reaped stale concurrency waiter",
						"key", w.Key, "run_id", w.RunID,
						"node_id", w.NodeID, "policy", w.Policy,
						"arrived_at", w.ArrivedAt.Format(time.RFC3339))
				}
			}
			if n, err := s.store.SweepLRUConcurrencyCache(ctx, s.concurrencyCacheCap); err != nil {
				s.logger.Error("concurrency cache LRU sweep failed", "err", err)
			} else if n > 0 {
				s.logger.Info("evicted LRU concurrency cache entries", "count", n)
			}
			// Node claims whose lease has expired: terminate as
			// failed with failure_reason=agent_lost. A clean failure
			// surfaces the problem; the orchestrator's Retry modifier
			// can redeliver intentionally.
			if pairs, err := s.store.FailExpiredNodeClaims(ctx); err != nil {
				s.logger.Error("node agent-lost sweep failed", "err", err)
			} else {
				for _, p := range pairs {
					s.logger.Warn("terminated node as agent_lost",
						"run_id", p[0], "node_id", p[1])
				}
			}
			// Queued nodes that no runner claimed before the queue
			// deadline: terminate with failure_reason=queue_timeout.
			// Protects against pools that drained or label sets that
			// nothing matches.
			if pairs, err := s.store.FailStaleQueuedNodes(ctx, s.queueTimeout); err != nil {
				s.logger.Error("queue-timeout sweep failed", "err", err)
			} else {
				for _, p := range pairs {
					s.logger.Warn("terminated node as queue_timeout",
						"run_id", p[0], "node_id", p[1])
				}
			}
			ids, err := s.store.ReapExpiredTriggers(ctx)
			if err != nil {
				s.logger.Error("reap failed", "err", err)
				continue
			}
			for _, id := range ids {
				// GetRun may miss if the dead worker never reached
				// CreateRun -- that's fine, no stale state to clean.
				run, err := s.store.GetRun(ctx, id)
				if err == nil && run.FinishedAt == nil {
					_ = s.store.FinishRun(ctx, id, "failed", "runner lease expired")
					// Cascade-fail nodes still marked running or
					// pending: the trigger lease expired, so any
					// orphaned node row is by definition stale.
					if nids, nerr := s.store.FailNodesInRun(ctx, id,
						"runner lease expired before node reported completion",
						store.FailureRunnerLeaseExpired); nerr != nil {
						s.logger.Error("cascade-fail nodes failed",
							"run_id", id, "err", nerr)
					} else {
						for _, nid := range nids {
							s.logger.Warn("cascade-failed orphan node",
								"run_id", id, "node_id", nid)
						}
					}
				}
				s.logger.Warn("reaped stale claim",
					"trigger_id", id,
					"had_run", err == nil,
				)
			}
			// Sample queue-depth + active-runner gauges on the
			// reaper's cadence. A stale gauge is preferable to a
			// crashed reaper.
			if n, err := s.store.CountPendingNodes(ctx); err != nil {
				s.logger.Error("pending nodes sample failed", "err", err)
			} else {
				setPendingNodes(n)
			}
			if n, err := s.store.CountActiveRunners(ctx, 2*time.Minute); err != nil {
				s.logger.Error("active runners sample failed", "err", err)
			} else {
				setActiveRunners(n)
			}
		}
	}
}

// withRequestLog records method, path, and status for every request
// and emits per-request Prometheus metrics against the normalized
// route pattern. The raw URL path only enters the log line, never a
// metric label, so cardinality stays bounded to the registered route
// set.
func withRequestLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(rw, r)
		elapsed := time.Since(start)
		route := normalizeRoute(r.URL.Path)
		observeHTTPRequest(route, r.Method, rw.status, elapsed)
		logger.Info("http",
			"method", r.Method,
			"path", r.URL.Path,
			"route", route,
			"status", rw.status,
			"dur_ms", elapsed.Milliseconds(),
		)
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
