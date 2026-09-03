package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Server owns the route table, the backing store, and the
// dispatcher. A single Server instance services all concurrent HTTP
// requests; the store itself serializes writes.
type Server struct {
	store      *store.Store
	dispatcher Dispatcher
	logger     *slog.Logger

	pool *poolBinding

	auth *Authenticator

	loginLimit *loginLimiter

	githubWebhookSecret  string
	githubWebhook        GitHubWebhookConfig
	githubCommitStatuses *githubCommitStatusReporter

	queueTimeout time.Duration

	sessionMaxLifetime time.Duration

	concurrencyCacheCap int

	secretsCipher Cipher

	costPerRunnerHour float64
	costRateSource    string

	bootstrapMu     sync.Mutex
	bootstrapExpiry time.Time
	bootstrapNeeded bool
	bootstrapClosed bool

	artifactStore storage.ArtifactStore

	cachePodURL string
	logsURL     string

	cacheURL string

	metricsAddr string

	reconcileHook func(context.Context) error

	runnerHeadroom *runnerHeadroomRegistry
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
		loginLimit:          newLoginLimiter(nil),
		queueTimeout:        15 * time.Minute,
		sessionMaxLifetime:  DefaultSessionMaxLifetime,
		concurrencyCacheCap: store.DefaultConcurrencyCacheCap,
		runnerHeadroom:      newRunnerHeadroomRegistry(),
	}
}

// WithSessionMaxLifetime caps how long a browser session lives from the
// moment it was created. A session past the cap is refused and deleted
// instead of renewed, so a polling tab cannot keep one alive forever.
// Zero or less removes the cap and restores an unbounded sliding TTL.
func (s *Server) WithSessionMaxLifetime(d time.Duration) *Server {
	s.sessionMaxLifetime = d
	return s
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

// WithArtifactStore enables in-process artifact serving at
// /api/v1/artifacts/{key}. The route is registered only when this
// option is set (laptop mode). Cluster mode serves artifacts from a
// dedicated process and leaves this nil.
func (s *Server) WithArtifactStore(a storage.ArtifactStore) *Server {
	s.artifactStore = a
	return s
}

// WithCachePodURL announces the externally-reachable sparkwing-cache
// pod URL via GET /api/v1/services so the operator CLI can discover
// it without configuring `gitcache:` in profiles.yaml. Empty disables
// the announcement (clients fall back to "no cache pod").
func (s *Server) WithCachePodURL(url string) *Server {
	s.cachePodURL = url
	return s
}

// WithLogsURL announces the externally-reachable sparkwing-logs URL
// via GET /api/v1/services, so a runner posts node log lines to the
// service that routes them instead of to the controller, which does
// not. Empty disables the announcement, which is correct for a
// co-located deployment where one mux serves both.
func (s *Server) WithLogsURL(url string) *Server {
	s.logsURL = url
	return s
}

// WithCacheURL configures the controller-to-cache proxy target used by
// gitcache seed and refresh routes.
func (s *Server) WithCacheURL(url string) *Server {
	s.cacheURL = url
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

// WithReconcileHook installs a function called before list-runs /
// get-run reads. Laptop mode passes a closure over
// orchestrator.ReconcileOrphanedLocalRuns so stale "running" rows
// from crashed in-process orchestrators get cleaned on the next
// dashboard refresh. Cluster mode leaves it nil; the cluster has its
// own reconciler.
//
// fn errors are intentionally swallowed by the wrapper -- a flaky
// sweep must never block a read.
func (s *Server) WithReconcileHook(fn func(context.Context) error) *Server {
	s.reconcileHook = fn
	return s
}

func (s *Server) reconcileBeforeRead(h http.HandlerFunc) http.HandlerFunc {
	if s.reconcileHook == nil {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		_ = s.reconcileHook(r.Context())
		h(w, r)
	}
}

// WithSecretsCipher binds the controller's secret encryption-at-rest
// cipher. A row written before the cipher was configured fails the
// read with 500; re-set those secrets through the API. Pass nil
// to keep the controller running unencrypted. The parameter is the
// local Cipher interface; any concrete type satisfying that method
// set works -- the default implementation lives in internal/secrets.
func (s *Server) WithSecretsCipher(c Cipher) *Server {
	s.secretsCipher = c
	return s
}

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

func (s *Server) markBootstrapClosed() {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	s.bootstrapClosed = true
	s.bootstrapNeeded = false
	s.bootstrapExpiry = time.Now().Add(60 * time.Second)
}

func (s *Server) authMiddleware() *Authenticator {
	if s.auth != nil {
		return s.auth
	}
	return NewAuthenticator(nil, 0).WithLogger(s.logger)
}

// safety: a live claim, not scope alone, decides who may write a node.
// Check and write use separate transactions, so expiry can admit one stale
// write; closing that gap requires threading the claimant through every mutation.
func (s *Server) claimedBy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.HasScope(ScopeAdmin) {
			next.ServeHTTP(w, r)
			return
		}
		runID, nodeID := r.PathValue("id"), r.PathValue("nodeID")
		claimant := claimIdentity(r)
		held, err := s.store.PrincipalHoldsNodeClaim(r.Context(), runID, nodeID, claimant, time.Now())
		if err == nil && !held {
			held, err = s.store.PrincipalHoldsTriggerClaim(r.Context(), runID, claimant, time.Now())
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !held {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:      "claim_required",
				Principal: p.label(),
				Message:   "node " + runID + "/" + nodeID + " is not claimed by this principal",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safety: a claim-scoped token touches only runs it is actually working on; admin bypasses.
func (s *Server) claimedRun(next http.Handler) http.Handler {
	return s.runMember("", next)
}

// safety: ownership is a live claim on one of the run's nodes or on the trigger the run came from.
func (s *Server) ownsRun(ctx context.Context, runID string, claimant store.ClaimIdentity) (bool, error) {
	held, err := s.store.PrincipalHoldsRunClaim(ctx, runID, claimant, time.Now())
	if err != nil || held {
		return held, err
	}
	return s.store.PrincipalHoldsTriggerClaim(ctx, runID, claimant, time.Now())
}

// safety: reads a runs.read token already gets stay open; a claim-only token is held to its own runs.
func (s *Server) readableRun(next http.Handler) http.Handler {
	return s.runMember(ScopeRunsRead, next)
}

func (s *Server) readableTrigger(next http.Handler) http.Handler {
	return s.runMember(ScopeTriggersRead, next)
}

func (s *Server) runMember(readerScope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.HasScope(ScopeAdmin) || (readerScope != "" && p.HasScope(readerScope)) {
			next.ServeHTTP(w, r)
			return
		}
		if readerScope != "" && !p.HasScope(ScopeNodesClaim) && !p.HasScope(ScopeTriggersClaim) {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:         "missing_scope",
				MissingScope: readerScope,
				Principal:    p.label(),
				Message:      "token lacks required scope: " + readerScope,
			})
			return
		}
		runID := r.PathValue("id")
		held, err := s.ownsRun(r.Context(), runID, claimIdentity(r))
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !held {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:      "claim_required",
				Principal: p.label(),
				Message:   "run " + runID + " is not claimed by this principal",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// safety: a pin outlives the run that sets it, so only a caller with a
// live claim on a run of that pipeline may write one; admin bypasses.
func (s *Server) claimedPipeline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.HasScope(ScopeAdmin) {
			next.ServeHTTP(w, r)
			return
		}
		pipeline := r.PathValue("name")
		held, err := s.store.PrincipalHoldsPipelineClaim(r.Context(), pipeline, claimIdentity(r), time.Now())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if !held {
			writeAuthError(w, http.StatusForbidden, authErrorBody{
				Code:      "claim_required",
				Principal: p.label(),
				Message:   "pipeline " + pipeline + " has no run claimed by this principal",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WithTrustedProxyCIDRs names the proxy source networks allowed to
// supply X-Forwarded-For for login throttling. Empty keys the login
// limiter on the TCP peer and ignores forwarded headers.
func (s *Server) WithTrustedProxyCIDRs(prefixes []netip.Prefix) *Server {
	s.loginLimit = newLoginLimiter(prefixes)
	if s.auth != nil {
		s.auth.WithTrustedProxyCIDRs(prefixes)
	}
	return s
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
// auth stays disabled (pass-through), and the server logs a loud
// warning so an operator has a signal that every endpoint is open.
//
// The tokens-table check happens ONCE at startup: a fresh row added
// via POST /api/v1/tokens takes effect on the next controller restart.
func (s *Server) EnableAuthFromStore() *Server {
	if !s.tokensTableNonEmpty() {
		s.auth = nil
		s.logger.Warn("controller serving unauthenticated: tokens table is empty, every endpoint is open; mint an admin token and restart to enable auth")
		return s
	}
	s.auth = NewAuthenticator(s.store, 60*time.Second).
		WithTrustedProxyCIDRs(s.loginLimit.trusted).
		WithLogger(s.logger)
	return s
}

// AuthEnabled reports whether the controller is enforcing bearer-token
// auth. False means every endpoint is served unauthenticated -- either
// laptop-local mode (auth never wired) or a cluster whose tokens table
// was empty at startup.
func (s *Server) AuthEnabled() bool {
	return s.auth != nil
}

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
// tests can wrap it in httptest without binding a real port. Callers using
// Handler directly must call Shutdown to drain server-owned background work.
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

	mux.Handle("POST /api/v1/runs", requireScope(ScopeRunsState, http.HandlerFunc(s.handleCreateRun)))
	mux.Handle("GET /api/v1/runs", requireScope(ScopeRunsRead, s.reconcileBeforeRead(s.handleListRuns)))
	mux.Handle("GET /api/v1/runs/{id}", requireScope(ScopeRunsRead, s.readableRun(s.reconcileBeforeRead(s.handleGetRun)), ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("GET /api/v1/runs/{id}/nodes", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodes)))
	mux.Handle("GET /api/v1/runs/{id}/receipt", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetRunReceipt)))
	mux.Handle("POST /api/v1/runs/{id}/finish", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleFinishRun))))
	mux.Handle("POST /api/v1/runs/{id}/plan", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleUpdatePlanSnapshot))))

	mux.Handle("POST /api/v1/runs/{id}/nodes", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleCreateNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/start", requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleStartNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/finish", requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleFinishNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/deps", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleUpdateNodeDeps))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}", requireScope(ScopeNodesClaim, s.readableRun(http.HandlerFunc(s.handleGetNode))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/output", requireScope(ScopeNodesClaim, s.readableRun(http.HandlerFunc(s.handleGetNodeOutput))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/dispatch", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleWriteNodeDispatch))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/dispatch", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetNodeDispatch)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/dispatches", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodeDispatches)))

	mux.Handle("POST /api/v1/runs/{id}/events", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleAppendEvent))))

	mux.Handle("POST /api/v1/triggers", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleTrigger)))
	mux.Handle("POST /api/v1/triggers/claim", requireScope(ScopeTriggersClaim, http.HandlerFunc(s.handleClaimTrigger)))
	mux.Handle("POST /api/v1/triggers/{id}/heartbeat", requireScope(ScopeTriggersClaim, http.HandlerFunc(s.handleHeartbeat)))
	mux.Handle("POST /api/v1/triggers/{id}/done", requireScope(ScopeTriggersClaim, http.HandlerFunc(s.handleFinishTrigger)))
	mux.Handle("GET /api/v1/triggers", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleListTriggers)))
	// hack: static segment prevents {id} from consuming "spawned-child" as a trigger ID.
	mux.Handle("GET /api/v1/triggers/spawned-child", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleFindSpawnedChildTrigger)))
	mux.Handle("GET /api/v1/triggers/{id}", requireScope(ScopeTriggersRead, s.readableTrigger(http.HandlerFunc(s.handleGetTrigger)), ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("POST /api/v1/gitcache/refresh", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleGitcacheRefresh)))
	mux.Handle("POST /api/v1/gitcache/seed", requireScope(ScopeAdmin, http.HandlerFunc(s.handleGitcacheSeed)))
	mux.Handle("POST /api/v1/gitcache/git/register", requireScope(ScopeAdmin, http.HandlerFunc(s.handleGitcacheRegister)))
	mux.Handle("GET /api/v1/gitcache/git/{path...}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleGitcacheGit)))
	mux.Handle("POST /api/v1/gitcache/git/{path...}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleGitcacheGit)))
	mux.Handle("POST /api/v1/runs/{id}/gitcache/git/register", requireScope(ScopeNodesClaim,
		s.claimedRun(http.HandlerFunc(s.handleGitcacheRegister))))
	mux.Handle("GET /api/v1/runs/{id}/gitcache/git/{path...}", requireScope(ScopeNodesClaim,
		s.claimedRun(http.HandlerFunc(s.handleGitcacheGit))))
	mux.Handle("POST /api/v1/runs/{id}/gitcache/git/{path...}", requireScope(ScopeNodesClaim,
		s.claimedRun(http.HandlerFunc(s.handleGitcacheGit))))

	mux.Handle("POST /api/v1/runs/{id}/cancel", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleCancelRun)))

	mux.Handle("GET /api/v1/trends", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleTrends)))
	mux.Handle("GET /api/v1/agents", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleAgents)))
	mux.Handle("PUT /api/v1/agents/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleEnrollAgent)))
	mux.Handle("POST /api/v1/agents/{name}/heartbeat", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleHeartbeatAgent)))

	mux.Handle("POST /api/v1/runs/{id}/retry", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleRetry)))
	mux.Handle("GET /api/v1/runs/{id}/attempts", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListAttempts)))

	mux.Handle("GET /api/v1/pipelines/{name}/latest", requireScope(ScopeRunsRead, http.HandlerFunc(s.handlePipelineLatest)))
	mux.Handle("GET /api/v1/pipelines/{name}/profile", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleGetPipelineProfile)))
	// safety: a pin becomes a hard Kubernetes limit for every later run of that pipeline,
	// so it is bound to a live claim on a run of that pipeline, not to the scope alone.
	mux.Handle("PUT /api/v1/pipelines/{name}/profile/pin", requireScope(ScopeRunsState, s.claimedPipeline(http.HandlerFunc(s.handleSetPipelinePin))))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/metrics", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAddNodeMetric))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/metrics", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetNodeMetrics)))

	mux.Handle("DELETE /api/v1/runs/{id}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteRun)))

	mux.Handle("POST /api/v1/concurrency/{key}/acquire", requireScope(ScopeAdmin, http.HandlerFunc(s.handleAcquireSlot)))
	mux.Handle("POST /api/v1/concurrency/{key}/heartbeat", requireScope(ScopeAdmin, http.HandlerFunc(s.handleHeartbeatSlot)))
	mux.Handle("POST /api/v1/concurrency/{key}/release", requireScope(ScopeAdmin, http.HandlerFunc(s.handleReleaseSlot)))
	mux.Handle("GET /api/v1/concurrency/{key}/holder", requireScope(ScopeAdmin, http.HandlerFunc(s.handleObserveSlot)))
	mux.Handle("GET /api/v1/concurrency/{key}/state", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleConcurrencyState)))
	mux.Handle("GET /api/v1/queue/state", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleQueueStateView)))
	mux.Handle("GET /api/v1/concurrency/{key}/notify", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleWaiterNotify)))
	mux.Handle("GET /api/v1/concurrency/{key}/resolve", requireScope(ScopeAdmin, http.HandlerFunc(s.handleResolveWaiter)))
	mux.Handle("POST /api/v1/concurrency/{key}/cancel-waiter", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCancelWaiter)))
	mux.Handle("POST /api/v1/concurrency/{key}/force-release", requireScope(ScopeAdmin, http.HandlerFunc(s.handleForceRelease)))

	mux.Handle("POST /api/v1/nodes/claim", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleClaimNode)))
	mux.Handle("POST /api/v1/nodes/claim/prepare", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handlePrepareNodeClaim)))
	// safety: readiness is a dispatcher decision; a runner token must not skip a node's dependencies.
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready", requireScope(ScopeAdmin, http.HandlerFunc(s.handleMarkNodeReady)))
	// safety: revoke-ready acts only on an unclaimed node, so claim ownership can never stand in for admin.
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/revoke-ready", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRevokeNodeReady)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/finalize-ready", requireScope(ScopeAdmin, http.HandlerFunc(s.handleFinalizeNodeReady)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleHeartbeatNodeClaim)))
	mux.Handle("POST /api/v1/runs/{id}/heartbeat", requireScope(ScopeNodesClaim, s.claimedRun(http.HandlerFunc(s.handleTouchRunHeartbeat))))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/activity", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleUpdateNodeActivity))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/touch", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleTouchNodeHeartbeat))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/annotations", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAppendNodeAnnotation))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/summary", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSetNodeSummary))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/artifact-manifest", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSetNodeArtifactManifest))))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleStartNodeStep))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleFinishNodeStep))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSkipNodeStep))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAppendStepAnnotation))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSetStepSummary))))
	mux.Handle("GET /api/v1/runs/{id}/steps", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodeSteps)))

	mux.Handle("POST /api/v1/runs/{id}/debug-pauses", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateDebugPause)))
	mux.Handle("GET /api/v1/runs/{id}/debug-pauses", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListDebugPauses)))
	mux.Handle("GET /api/v1/runs/{id}/paused", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListDebugPauses)))

	mux.Handle("GET /api/v1/runs/{id}/events", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListEvents)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetActiveDebugPause)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/release", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleReleaseDebugPause)))
	// safety: nodes.claim may consume only the bounce request already assigned
	// to that supervising runner; creating one still requires runs.write.
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/bounce", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleRequestNodeBounce)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/bounce", requireScope(ScopeNodesClaim, s.readableRun(http.HandlerFunc(s.handlePendingNodeBounce))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/bounce/consume", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleConsumeNodeBounce))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/status", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleSetNodeStatus))))

	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}/request", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRequestApproval)))
	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeApprovalsWrite, http.HandlerFunc(s.handleResolveApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListApprovalsForRun)))
	mux.Handle("GET /api/v1/approvals/pending", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListPendingApprovals)))

	if s.pool != nil {
		mux.Handle("GET /api/v1/pool", requireScope(ScopeRunsRead, http.HandlerFunc(s.handlePoolList)))
		mux.Handle("POST /api/v1/pool/checkout", requireScope(ScopeAdmin, http.HandlerFunc(s.handlePoolCheckout)))
		mux.Handle("POST /api/v1/pool/return", requireScope(ScopeAdmin, http.HandlerFunc(s.handlePoolReturn)))
		mux.Handle("POST /api/v1/pool/heartbeat", requireScope(ScopeAdmin, http.HandlerFunc(s.handlePoolHeartbeat)))
	}

	if s.artifactStore != nil {
		mux.Handle("GET /api/v1/artifacts/{key}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleArtifactGet)))
	}

	mux.Handle("POST /api/v1/tokens", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateToken)))
	mux.Handle("GET /api/v1/tokens", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListTokens)))
	mux.Handle("GET /api/v1/tokens/{prefix}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleLookupTokenByPrefix)))
	mux.Handle("DELETE /api/v1/tokens/{prefix}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRevokeToken)))

	mux.Handle("GET /api/v1/auth/whoami", http.HandlerFunc(s.handleWhoami))

	// safety: service discovery names internal cache and logs URLs, so any bearer will do but anonymity will not.
	mux.Handle("GET /api/v1/services", http.HandlerFunc(s.handleServices))

	mux.Handle("POST /api/v1/tokens/{prefix}/rotate", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRotateToken)))

	mux.Handle("GET /api/v1/users", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListUsers)))
	mux.Handle("POST /api/v1/users", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateUserOrBootstrap)))
	mux.Handle("DELETE /api/v1/users/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteUser)))

	mux.Handle("POST /api/v1/secrets", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateSecret)))
	mux.Handle("GET /api/v1/secrets", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListSecrets)))
	mux.Handle("GET /api/v1/secrets/{name}", requireScope(ScopeSecretsRead, http.HandlerFunc(s.handleGetSecret)))
	mux.Handle("DELETE /api/v1/secrets/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteSecret)))

	authed := s.authMiddleware().Middleware(mux)

	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	router.Handle("POST /api/v1/auth/login", s.loginLimit.middleware(http.HandlerFunc(s.handleLogin)))
	router.Handle("POST /api/v1/auth/logout", http.HandlerFunc(s.handleLogout))
	router.Handle("GET /api/v1/auth/session", http.HandlerFunc(s.handleSession))
	router.Handle("GET /api/v1/auth/bootstrap-needed", http.HandlerFunc(s.handleBootstrapNeeded))
	if s.metricsAddr == "" {
		router.Handle("GET /metrics", metricsHandler())
	}
	router.Handle("POST /webhooks/github/{pipeline}", http.HandlerFunc(s.handleGitHubWebhook))
	router.Handle("/", authed)

	return withStreamDeadlineControl(otelutil.WrapHandler("sparkwing-controller", withRequestLog(router, s.logger)))
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

	if n, err := store.Maintenance.ReconcileConcurrencyKeys(s.store, ctx, store.DefaultConcurrencyLease); err != nil {
		s.logger.Warn("concurrency reconcile on startup failed", "err", err)
	} else if n > 0 {
		s.logger.Info("concurrency reconcile promoted stranded waiters", "count", n)
	}

	go s.runReaper(ctx, 10*time.Second)

	if s.pool != nil {
		go s.pool.run(ctx, s.logger)
	}

	errCh := make(chan error, 2)

	metricsSrv := s.metricsServer()
	if metricsSrv != nil {
		// safety: a metrics endpoint that never binds leaves the operator blind, so refuse to start instead.
		metricsLn, err := net.Listen("tcp", metricsSrv.Addr)
		if err != nil {
			return fmt.Errorf("controller metrics listener: %w", err)
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

	go func() {
		s.logger.Info("controller listening", "addr", addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			s.logger.Warn("controller HTTP shutdown incomplete", "err", err)
		}
		s.shutdownGitHubCommitStatuses(shutdownCtx)
		return nil
	case err := <-errCh:
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.shutdownGitHubCommitStatuses(shutdownCtx)
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) shutdownGitHubCommitStatuses(ctx context.Context) {
	if err := s.Shutdown(ctx); err != nil {
		s.logger.Warn("github commit status shutdown incomplete", "err", err)
	}
}

// Shutdown drains server-owned background work until ctx expires. ServeWith
// calls Shutdown automatically; callers serving Handler directly must call it.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.githubCommitStatuses == nil {
		return nil
	}
	return s.githubCommitStatuses.shutdown(ctx)
}

func (s *Server) runReaper(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if stale, err := store.Maintenance.ReapStaleConcurrencyHolders(s.store, ctx); err != nil {
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
			if n, err := store.Maintenance.SweepExpiredConcurrencyCache(s.store, ctx); err != nil {
				s.logger.Error("concurrency cache TTL sweep failed", "err", err)
			} else if n > 0 {
				s.logger.Info("swept expired concurrency cache entries", "count", n)
			}
			if dropped, err := store.Maintenance.ReapStaleConcurrencyWaiters(s.store, ctx, 2*store.DefaultConcurrencyLease); err != nil {
				s.logger.Error("concurrency waiter reap failed", "err", err)
			} else {
				for _, w := range dropped {
					s.logger.Warn("reaped stale concurrency waiter",
						"key", w.Key, "run_id", w.RunID,
						"node_id", w.NodeID, "policy", w.Policy,
						"arrived_at", w.ArrivedAt.Format(time.RFC3339))
				}
			}
			if n, err := store.Maintenance.SweepLRUConcurrencyCache(s.store, ctx, s.concurrencyCacheCap); err != nil {
				s.logger.Error("concurrency cache LRU sweep failed", "err", err)
			} else if n > 0 {
				s.logger.Info("evicted LRU concurrency cache entries", "count", n)
			}
			if pairs, err := store.Maintenance.FailExpiredNodeClaims(s.store, ctx); err != nil {
				s.logger.Error("node agent-lost sweep failed", "err", err)
			} else {
				for _, p := range pairs {
					s.logger.Warn("terminated node as agent_lost",
						"run_id", p[0], "node_id", p[1])
				}
			}
			if pairs, err := store.Maintenance.FailStaleQueuedNodes(s.store, ctx, s.queueTimeout); err != nil {
				s.logger.Error("queue-timeout sweep failed", "err", err)
			} else {
				for _, p := range pairs {
					s.logger.Warn("terminated node as queue_timeout",
						"run_id", p[0], "node_id", p[1])
				}
			}
			ids, err := store.Maintenance.ReapExpiredTriggers(s.store, ctx)
			if err != nil {
				s.logger.Error("reap failed", "err", err)
				continue
			}
			for _, id := range ids {
				run, err := s.store.GetRun(ctx, id)
				if err == nil && run.FinishedAt == nil {
					if ferr := s.store.FinishRun(ctx, id, "failed", "runner lease expired"); ferr != nil {
						s.logger.Error("finish reaped run failed", "run_id", id, "err", ferr)
					} else {
						s.reportGitHubCommitStatus(ctx, id, "failed")
					}
					if nids, nerr := store.Maintenance.FailNodesInRun(s.store, ctx, id,
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
				s.logger.Warn(
					"reaped stale claim",
					"trigger_id", id,
					"had_run", err == nil,
				)
			}
			if ids, err := store.Maintenance.ReapStalePendingRuns(s.store, ctx,
				5*store.DefaultLeaseDuration,
				"reaped: trigger consumer finished without dispatching the pipeline"); err != nil {
				s.logger.Error("stale pending sweep failed", "err", err)
			} else {
				for _, id := range ids {
					s.logger.Warn("reaped stale pending run", "run_id", id)
					s.reportGitHubCommitStatus(ctx, id, "failed")
				}
			}

			if ids, err := store.Maintenance.ReapStaleRunningRuns(s.store, ctx,
				3*time.Minute,
				"reaped: no run-level heartbeat for >3m; orchestrator is no longer running"); err != nil {
				s.logger.Error("stale running sweep failed", "err", err)
			} else {
				for _, id := range ids {
					s.logger.Warn("reaped stale running run", "run_id", id)
					s.reportGitHubCommitStatus(ctx, id, "failed")
				}
			}

			if pairs, err := store.Maintenance.ReapTimedOutApprovals(s.store, ctx); err != nil {
				s.logger.Error("approval timeout sweep failed", "err", err)
			} else {
				for _, p := range pairs {
					s.logger.Warn("reaped timed-out approval",
						"run_id", p[0], "node_id", p[1])
				}
			}

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

func withRequestLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		writer := http.ResponseWriter(rw)
		if _, ok := w.(http.Flusher); ok {
			writer = &flushingStatusRecorder{statusRecorder: rw}
		}
		start := time.Now()
		next.ServeHTTP(writer, r)
		elapsed := time.Since(start)
		route := normalizeRoute(r.URL.Path)
		observeHTTPRequest(route, r.Method, rw.status, elapsed)
		logger.Info(
			"http",
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

type flushingStatusRecorder struct {
	*statusRecorder
}

func (r *flushingStatusRecorder) Flush() {
	r.ResponseWriter.(http.Flusher).Flush()
}
