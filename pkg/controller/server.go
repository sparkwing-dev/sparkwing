package controller

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/sparkwing-dev/sparkwing/internal/backend"
	"github.com/sparkwing-dev/sparkwing/internal/egress"
	"github.com/sparkwing-dev/sparkwing/internal/mailer"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/internal/teamblob"
	"github.com/sparkwing-dev/sparkwing/internal/web"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Server owns the route table, the backing store, and the
// dispatcher. A single Server instance services all concurrent HTTP
// requests; the store itself serializes writes.
type Server struct {
	store  *store.Store
	logger *slog.Logger

	hostKeyScan           func(ctx context.Context, host string, port int) (ssh.PublicKey, error)
	sourceCredentialLimit perMinuteLimiter
	identityLinkLimit     perMinuteLimiter

	auth *Authenticator

	peerPrincipal func(*http.Request) *Principal

	loginLimit *loginLimiter

	flood         *floodControl
	requestBudget *principalBudget
	tokenBudget   *tokenBudget

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

	artifactStore storage.ArtifactStore

	bucketUsageStore storage.ArtifactStore
	storagePass      *storagePass
	downloadStores   map[store.StorageKind]*teamblob.Store
	downloadS3       *s3.PresignClient
	downloadCDN      *sign.URLSigner
	downloadDomain   string
	directUploads    *directUploadS3
	outputSigner     outputSigner
	logRetention     map[store.Team]int
	logLifecycle     logLifecycle
	egress           *egress.Meter
	// safety: the reaper goroutine is this field's only reader and
	// writer, which is what lets the once-a-month prune gate skip a lock.
	egressPrunedMonth string

	cachePodURL  string
	dashboardURL string
	externalURL  string
	oidc         oidcState

	cacheURL                     string
	cacheGrantKey                string
	downloadFree, downloadFunded int64

	teamStorage        TeamStorage
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
	routeProbePub  *http.ServeMux

	storage storageSample

	localExecution bool

	identity identityConfig

	tenants tenantCache

	githubRunners githubRunnerConfig

	githubApp *githubAppState
	checkout  *billingCheckout
	operators atomic.Pointer[map[string]bool]

	dashboard *Dashboard
}

// WithLocalExecution marks this server as a host's own admission daemon or
// loopback controller, the only place a node executed in the caller's own
// process can report itself. It is what lets the execution-attempt routes
// accept an attempt with executor kind "local", which carries no claim
// identity; a cluster controller leaves it unset, so a runs.write caller
// cannot forge attribution or ordinals on a node it never ran.
func (s *Server) WithLocalExecution() *Server {
	s.localExecution = true
	// safety: a host's own controller serves one machine's runs, where a widened
	// idle poll costs pickup latency and a shed claim has no fleet to protect;
	// its callers are unauthenticated, so every runner would share one bucket.
	s.idleClaimPoll = 0
	s.idlePolls = nil
	s.requestBudget = newPrincipalBudget(RequestBudget{})
	s.tokenBudget = nil
	return s
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
		outputSigner:       newOutputSigner(),
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

// WithBucketUsage names the object store the bucket ceiling measures.
// The store is read on the reconciliation interval and never served, so
// pointing the controller at the bucket its runners write to gives the
// ceiling a measured total without exposing those objects on any route.
// Without one the ceiling counts only the writes this process made.
func (s *Server) WithBucketUsage(a storage.ArtifactStore) *Server {
	s.bucketUsageStore = a
	return s
}

// WithArtifactStore names the artifact store the bucket ceiling measures
// when no bucket usage store is set (laptop mode). Cluster mode leaves it
// nil.
func (s *Server) WithArtifactStore(a storage.ArtifactStore) *Server {
	s.artifactStore = a
	return s
}

// WithCachePodURL announces the externally-reachable sparkwing-cache
// pod URL via GET /api/v1/services so the operator CLI can discover
// it without configuring `gitcache:` in config.yaml. Empty disables
// the announcement (clients fall back to "no cache pod").
func (s *Server) WithCachePodURL(url string) *Server {
	s.cachePodURL = url
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

// WithCacheURL configures the controller-to-cache proxy target used by the
// gitcache registration and clone routes.
func (s *Server) WithCacheURL(url string) *Server {
	s.cacheURL = url
	return s
}

// WithCacheGrantKey sets the key the controller signs cache grants with and
// verifies a grant presented to it with. Empty mints and accepts no grants.
func (s *Server) WithCacheGrantKey(key string) *Server {
	s.cacheGrantKey = key
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

// WithSecretsCipher binds the controller's secret encryption-at-rest
// cipher. A row written before the cipher was configured fails the
// read with 500; re-set those secrets through the API. Pass nil
// to keep the controller running unencrypted. The parameter is the
// local Cipher interface; any concrete type satisfying that method
// set works -- the default implementation lives in internal/secrets.
func (s *Server) WithSecretsCipher(c Cipher) *Server {
	s.secretsCipher = c
	if s.store != nil {
		bound, _ := c.(store.SessionKeyCipher)
		s.store.SetSessionKeyCipher(bound)
	}
	return s
}

func (s *Server) authMiddleware() *Authenticator {
	if s.auth != nil {
		return s.auth
	}
	return NewAuthenticator(nil, 0).WithLogger(s.logger)
}

func (s *Server) withTriggerClaimFence(next http.Handler) http.Handler {
	return s.claimedTrigger(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, ok := s.triggerClaimRequest(w, r, r.PathValue("id"))
		if ok {
			next.ServeHTTP(w, r)
		}
	}))
}

func (s *Server) triggerClaimRequest(w http.ResponseWriter, r *http.Request, runID string) (*http.Request, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.HasScope(ScopeAdmin) {
		return r, true
	}
	hasNodeIdentity, hasTriggerIdentity := claimIdentityShape(r)
	if hasNodeIdentity || !hasTriggerIdentity {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " requires its exact trigger generation",
		})
		return r, false
	}
	generation, err := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
	if err != nil || generation < 1 {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return r, false
	}
	fence := store.TriggerClaimFence{Claimant: claimIdentity(r), ClaimGeneration: generation}
	return r.WithContext(store.WithTriggerClaimFence(r.Context(), fence)), true
}

func (s *Server) runClaimRequest(w http.ResponseWriter, r *http.Request, runID string) (*http.Request, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.HasScope(ScopeAdmin) {
		return r, true
	}
	hasNodeIdentity, hasTriggerIdentity := claimIdentityShape(r)
	if hasNodeIdentity == hasTriggerIdentity {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " requires one exact claim identity",
		})
		return r, false
	}
	held, err := s.ownsRun(r.Context(), runID, claimIdentity(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return r, false
	}
	if !held {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "run " + runID + " is not claimed by this principal",
		})
		return r, false
	}
	if hasNodeIdentity {
		fence, err := nodeClaimFenceFromRequest(r)
		if err != nil {
			writeError(w, http.StatusConflict, store.ErrLockHeld)
			return r, false
		}
		return r.WithContext(store.WithNodeClaimFence(r.Context(), fence)), true
	}
	generation, err := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
	if err != nil || generation < 1 {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return r, false
	}
	fence := store.TriggerClaimFence{Claimant: claimIdentity(r), ClaimGeneration: generation}
	return r.WithContext(store.WithTriggerClaimFence(r.Context(), fence)), true
}

// safety: a live claim, not scope alone, decides who may write a node.
func (s *Server) claimedBy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		validated, ok := s.nodeTargetWriteRequest(w, r, r.PathValue("id"), r.PathValue("nodeID"))
		if ok {
			next.ServeHTTP(w, validated)
		}
	})
}

// safety: admin skips the claim check, but a trigger-owned execution still
// names its generation, and the handlers route on that fence.
func adminTriggerFence(r *http.Request) *http.Request {
	nodeClaim, triggerClaim := claimIdentityShape(r)
	if nodeClaim || !triggerClaim {
		return r
	}
	generation, err := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
	if err != nil || generation < 1 {
		return r
	}
	return r.WithContext(store.WithTriggerClaimFence(r.Context(), store.TriggerClaimFence{
		Claimant: claimIdentity(r), ClaimGeneration: generation,
	}))
}

func claimIdentityShape(r *http.Request) (node, trigger bool) {
	for _, name := range []string{
		store.ClaimHolderHeader,
		store.ClaimMembershipHeader,
		store.ClaimReservationHeader,
		store.ClaimGenerationHeader,
	} {
		node = node || r.Header.Get(name) != ""
	}
	return node, r.Header.Get(store.TriggerGenerationHeader) != ""
}

func (s *Server) nodeTargetWriteRequest(
	w http.ResponseWriter, r *http.Request, runID, nodeID string,
) (*http.Request, bool) {
	p, ok := PrincipalFromContext(r.Context())
	if !ok || p.HasScope(ScopeAdmin) {
		return adminTriggerFence(r), true
	}
	claimRequired := func() bool {
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code: "claim_required", Principal: p.label(),
			Message: "node " + runID + "/" + nodeID + " requires one exact live claim",
		})
		return false
	}
	nodeClaim, triggerClaim := claimIdentityShape(r)
	if nodeClaim == triggerClaim || (nodeClaim && nodeID == "") {
		return r, claimRequired()
	}
	if nodeClaim {
		fence, fenceErr := nodeClaimFenceFromRequest(r)
		if fenceErr != nil {
			return r, claimRequired()
		}
		held, err := s.store.NodeClaimFenceIsLive(r.Context(), runID, nodeID, fence, time.Now())
		if err != nil {
			s.writeInternalError(w, r, "validate node claim",
				fmt.Errorf("node %s/%s: %w", runID, nodeID, err))
			return r, false
		}
		if !held {
			writeError(w, http.StatusConflict, store.ErrLockHeld)
			return r, false
		}
		return r.WithContext(store.WithNodeClaimFence(r.Context(), fence)), true
	}
	generation, parseErr := strconv.ParseInt(r.Header.Get(store.TriggerGenerationHeader), 10, 64)
	if parseErr != nil || generation < 1 {
		return r, claimRequired()
	}
	triggerFence := store.TriggerClaimFence{Claimant: claimIdentity(r), ClaimGeneration: generation}
	held, err := s.store.TriggerClaimFenceIsLive(
		r.Context(), runID, triggerFence.Claimant, generation, time.Now())
	if err != nil {
		s.writeInternalError(w, r, "validate trigger claim", fmt.Errorf("run %s: %w", runID, err))
		return r, false
	}
	if !held {
		writeError(w, http.StatusConflict, store.ErrLockHeld)
		return r, false
	}
	return r.WithContext(store.WithTriggerClaimFence(r.Context(), triggerFence)), true
}

func (s *Server) authorizeTriggerParent(w http.ResponseWriter, r *http.Request, runID, nodeID string) bool {
	if runID == "" {
		return true
	}
	// safety: the validated fence belongs to the parent. Attaching it to the
	// request would make the new child's store writes answer to the wrong row.
	_, ok := s.nodeTargetWriteRequest(w, r, runID, nodeID)
	return ok
}

func nodeClaimFenceFromRequest(r *http.Request) (store.NodeClaimFence, error) {
	generation, err := strconv.ParseInt(r.Header.Get(store.ClaimGenerationHeader), 10, 64)
	fence := store.NodeClaimFence{
		Claimant: claimIdentity(r), HolderID: r.Header.Get(store.ClaimHolderHeader),
		MembershipID:  r.Header.Get(store.ClaimMembershipHeader),
		ReservationID: r.Header.Get(store.ClaimReservationHeader), ClaimGeneration: generation,
	}
	if err != nil || generation < 1 || fence.HolderID == "" {
		return store.NodeClaimFence{}, errors.New("exact node claim fence is required")
	}
	return fence, nil
}

// safety: a claim-scoped token touches only runs it is actually working on; admin bypasses.
func (s *Server) claimedRun(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, ok := s.triggerClaimRequest(w, r, r.PathValue("id"))
		if ok {
			next.ServeHTTP(w, r)
		}
	})
}

func (s *Server) claimedRunAccess(next http.Handler) http.Handler {
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

// safety: a profile outlives the run that writes it, so a write needs a live claim
// on a run of that pipeline: a node claim for a runner executing one node, or the
// run's trigger claim for the orchestrator, which records the wait before the first
// node exists and the measurement after the last one is gone. Admin bypasses.
func (s *Server) claimedPipeline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok || p.HasScope(ScopeAdmin) {
			next.ServeHTTP(w, r)
			return
		}
		pipeline := r.PathValue("name")
		held, err := s.store.PrincipalHoldsProfileClaim(r.Context(), pipeline, claimIdentity(r), time.Now())
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

// WithTrustedProxyAddr serves the same API on a second address whose
// requests key login throttling, the bearer failure budget and the
// request log on their X-Real-IP. Only a proxy that overwrites that
// header may reach addr.
func (s *Server) WithTrustedProxyAddr(addr string) *Server {
	s.trustedProxyAddr = addr
	return s
}

// EnableAuthFromStore wires the Authenticator against the server's
// tokens table IF the table has any non-revoked rows. Empty table =
// auth stays disabled (pass-through), and the server logs a loud
// warning so an operator has a signal that every endpoint is open.
// A multi-team controller is the exception: see requireAuthForTeams.
//
// The tokens-table check happens ONCE at startup: a fresh row added
// via POST /api/v1/tokens takes effect on the next controller restart.
func (s *Server) EnableAuthFromStore() *Server {
	if !s.tokensTableNonEmpty() && !s.MultiTeam() {
		s.auth = nil
		s.logger.Warn("controller serving unauthenticated: tokens table is empty, every endpoint is open; mint an admin token and restart to enable auth")
		return s
	}
	if !s.tokensTableNonEmpty() {
		s.logger.Warn("multi-team controller requires authentication with an empty tokens table: " +
			"only signed-in sessions are accepted until an admin token exists; supply one with " +
			"the bootstrap-admin-token file in the controller's --credentials-dir")
	}
	s.auth = s.storeAuthenticator()
	return s
}

// safety: a revocation on another replica reaches this one at most this late.
const tokenCacheTTL = 60 * time.Second

func (s *Server) storeAuthenticator() *Authenticator {
	return NewAuthenticator(s.store, tokenCacheTTL).
		WithLogger(s.logger)
}

// safety: a request with no credential acts as the default team's operator, which on a multi-team
// controller is every team's operator, so the multi-team license forces token auth. It runs
// again when the handler is built because the license may be installed after EnableAuthFromStore.
func (s *Server) requireAuthForTeams() {
	if s.auth == nil && s.store != nil && s.MultiTeam() {
		s.auth = s.storeAuthenticator()
	}
}

// AuthEnabled reports whether the controller is enforcing bearer-token
// auth. False means every endpoint is served unauthenticated -- either
// laptop-local mode (auth never wired) or a single-team cluster whose
// tokens table was empty at startup.
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

// WithPeerPrincipal authenticates a request that carries no Authorization
// header by the transport it arrived on, which is how a controller served
// over a unix socket treats the peer's uid as the identity. fn returns nil to
// refuse the request with 401. A request that does carry a bearer token still
// goes to the Authenticator, so a token keeps working on the same listener.
func (s *Server) WithPeerPrincipal(fn func(*http.Request) *Principal) *Server {
	s.peerPrincipal = fn
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
	api := s.handler(s.handleFinishRun)
	if s.dashboard == nil {
		return api
	}
	return s.browserHandler(api)
}

// DaemonHandler leaves profile recording to the host orchestrator. Clients
// completing runs through this handler must submit profile observations separately.
// Call Shutdown to drain background work before exiting.
func (s *Server) DaemonHandler() http.Handler {
	return s.handler(s.handleFinishDaemonRun)
}

func (s *Server) handler(finishRun http.HandlerFunc) http.Handler {
	s.requireAuthForTeams()
	mux, router := s.routers(finishRun)
	router.Handle("/", s.authenticated(mux, s.githubRunnerFence(s.hereRunnerFence(s.tokenBudgeted(s.teamBoundary(mux, unsupportedRouteFallback(mux)))))))
	h := withStreamDeadlineControl(otelutil.WrapHandler("sparkwing-controller",
		withRequestLog(router, s.logger, muxRouteLabeler(router, mux), func(r *http.Request) string {
			return ratelimit.ClientIP(r)
		})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.Metering() {
			path := r.URL.Path
			if strings.HasPrefix(path, "/api/v1/credits") ||
				strings.HasPrefix(path, "/api/v1/team/billing") ||
				(strings.HasPrefix(path, "/api/v1/tokens/") && strings.HasSuffix(path, "/metered")) {
				http.NotFound(w, r)
				return
			}
			r = r.WithContext(store.WithoutCreditMetering(r.Context()))
		}
		h.ServeHTTP(w, r)
	})
}

// safety: neither mux is wired to the other and no handler runs, so a caller
// that only needs to know whether a route exists can build these from a zero
// Server without reaching a store.
func (s *Server) routers(finishRun http.HandlerFunc) (authed, public *http.ServeMux) {
	mux := http.NewServeMux()

	mux.Handle("POST /api/v1/runs", requireScope(ScopeRunsState, http.HandlerFunc(s.handleCreateRun)))
	mux.Handle("GET /api/v1/runs", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListRuns)))
	mux.Handle("GET /api/v1/runs/{id}", newClaimSensitiveRoute(claimSourceKinds, nil, http.HandlerFunc(s.handleGetRun)).orElse(requireScope(ScopeRunsRead, s.readableRun(http.HandlerFunc(s.handleGetRun)), ScopeNodesClaim, ScopeTriggersClaim)))
	mux.Handle("GET /api/v1/runs/{id}/nodes", requireScope(ScopeRunsRead, s.readableRun(http.HandlerFunc(s.handleListNodes)), ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("GET /api/v1/runs/{id}/receipt", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetRunReceipt)))
	mux.Handle("GET /api/v1/runs/{id}/pending-triggers", requireScope(ScopeTriggersRead, s.readableTrigger(http.HandlerFunc(s.handleListPendingTriggersForParent)), ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("POST /api/v1/runs/{id}/finish", requireScope(ScopeRunsState, s.claimedRun(finishRun)))
	mux.Handle("POST /api/v1/runs/{id}/plan", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleUpdatePlanSnapshot))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/attempt", s.newClaimResultRoute([]store.ClaimTokenKind{store.ClaimTokenPlan, store.ClaimTokenWork}, nil, s.handleReportAttempt))

	mux.Handle("POST /api/v1/runs/{id}/nodes", requireScope(ScopeRunsState, s.claimedRun(http.HandlerFunc(s.handleCreateNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/start", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleStartNode)).orElse(requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleStartNode)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/finish", requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleFinishNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/deps", requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleUpdateNodeDeps))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}", newClaimSensitiveRoute(claimWorkKinds, nil, http.HandlerFunc(s.handleGetNode)).orElse(requireScope(ScopeNodesClaim, s.readableRun(http.HandlerFunc(s.handleGetNode)))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/output", newClaimSensitiveRoute(claimWorkKinds, nil, http.HandlerFunc(s.handleGetNodeOutput)).orElse(requireScope(ScopeNodesClaim, s.readableRun(http.HandlerFunc(s.handleGetNodeOutput)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/output-upload", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleOutputUpload)).orElse(requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleOutputUpload)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/log-segments", newClaimLogTailRoute(claimLogKinds, http.HandlerFunc(s.handleLogSegments)).orElse(requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleLogSegments)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/log-manifest", newClaimLogTailRoute(claimLogKinds, http.HandlerFunc(s.handleLogManifest)).orElse(requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleLogManifest)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/output-commit", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleOutputCommit)).orElse(requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleOutputCommit)))))
	mux.Handle("POST /api/v1/runs/{id}/children", newClaimSensitiveRoute(claimWorkKinds, nil, http.HandlerFunc(s.handleEnqueueChildRun)))
	mux.Handle("GET /api/v1/runs/{id}/children/{childID}", newClaimSensitiveRoute(claimWorkKinds, nil, http.HandlerFunc(s.handleGetChildRun)))
	mux.Handle("GET /api/v1/runs/{id}/children/{childID}/nodes/{nodeID}/output", newClaimSensitiveRoute(claimWorkKinds, nil, http.HandlerFunc(s.handleGetChildNodeOutput)))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/logs", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleAppendNodeLiveLog)).orElse(requireScope(ScopeRunsState, s.claimedBy(http.HandlerFunc(s.handleAppendNodeLiveLog)))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/logs", requireScope(ScopeRunsRead, s.metered(egress.ClassLog, s.readableRun(http.HandlerFunc(s.handleReadNodeLiveLog))), ScopeLogsRead, ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/logs/stream", requireScope(ScopeRunsRead, s.meteredStream(egress.ClassLogStream, s.readableRun(http.HandlerFunc(s.handleStreamNodeLiveLog))), ScopeLogsRead, ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("GET /api/v1/runs/{id}/logs", requireScope(ScopeLogsRead, s.metered(egress.ClassLog, s.dashboardRead(web.RunLogsHandler))))
	mux.Handle("GET /api/v1/runs/{id}/logs/search", requireScope(ScopeLogsRead, s.dashboardRead(func(b backend.Backend) http.HandlerFunc {
		return web.RunLogsSearchHandler(b, controllerSearchBounds)
	})))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}", requireScope(ScopeLogsRead, s.metered(egress.ClassLog, s.dashboardRead(web.NodeLogsHandler))))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}/stream", requireScope(ScopeLogsRead, s.meteredStream(egress.ClassLogStream, s.dashboardRead(web.NodeLogStreamHandler))))
	mux.Handle("GET /api/v1/runs/{id}/logs/{node}/completeness", requireScope(ScopeLogsRead, s.dashboardRead(web.NodeLogCompletenessHandler)))
	mux.Handle("GET /api/v1/runs/grep", requireScope(ScopeLogsRead, s.dashboardRead(func(b backend.Backend) http.HandlerFunc {
		return web.RunsGrepHandler(b, controllerSearchBounds)
	})))
	mux.Handle("GET /api/v1/runs/{id}/events/stream", requireScope(ScopeRunsRead, s.dashboardRead(web.EventsStreamHandler)))
	mux.Handle("GET /api/v1/capacity/profiles", requireScope(ScopeRunsRead, s.capacityRead(web.CapacityProfilesHandler)))
	mux.Handle("GET /api/v1/capacity/profiles/explain", requireScope(ScopeRunsRead, s.capacityRead(web.CapacityExplainHandler)))

	mux.Handle("POST /api/v1/runs/{id}/events", requireScope(ScopeRunsState, http.HandlerFunc(s.handleAppendEvent)))

	mux.Handle("POST /api/v1/triggers", requireScope(ScopeRunsWrite, http.HandlerFunc(s.handleTrigger)))
	mux.Handle("POST /api/v1/triggers/claim", requireScope(ScopeTriggersClaim, s.idlePollBudgeted(http.HandlerFunc(s.handleClaimTrigger))))
	mux.Handle("POST /api/v1/triggers/{id}/heartbeat", requireScope(ScopeTriggersClaim, s.withTriggerClaimFence(http.HandlerFunc(s.handleHeartbeat))))
	mux.Handle("POST /api/v1/triggers/{id}/done", requireScope(ScopeTriggersClaim, s.withTriggerClaimFence(http.HandlerFunc(s.handleFinishTrigger))))
	mux.Handle("GET /api/v1/triggers", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleListTriggers)))
	// hack: static segment prevents {id} from consuming "spawned-child" as a trigger ID.
	mux.Handle("GET /api/v1/triggers/spawned-child", requireScope(ScopeTriggersRead, http.HandlerFunc(s.handleFindSpawnedChildTrigger)))
	mux.Handle("POST /api/v1/triggers/{id}/claim", requireScope(ScopeTriggersClaim, s.claimBudgeted(http.HandlerFunc(s.handleClaimSpecificTrigger))))
	mux.Handle("GET /api/v1/triggers/{id}", requireScope(ScopeTriggersRead, s.readableTrigger(http.HandlerFunc(s.handleGetTrigger)), ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("POST /api/v1/runs/{id}/cache-grant", newClaimSensitiveRoute(claimSourceKinds, nil, s.handleRunCacheGrant(s.runTeam)).orElse(requireScope(ScopeNodesClaim, s.handleRunCacheGrant(s.runTeam), ScopeTriggersClaim)))
	mux.Handle("POST /api/v1/runs/{id}/source-credential", newClaimSensitiveRoute(claimSourceCredentialKinds, nil, http.HandlerFunc(s.handleRunSourceCredential)))
	mux.Handle("POST /api/v1/runs/{id}/oidc-token", newClaimSensitiveRoute(claimWorkKinds, nil, http.HandlerFunc(handleClaimOIDCToken)).orElse(requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleOIDCToken), ScopeTriggersClaim)))
	mux.Handle("POST /api/v1/gitcache/git/register", requireScope(ScopeAdmin, http.HandlerFunc(s.handleGitcacheRegister)))
	mux.Handle("GET /api/v1/gitcache/git/{path...}", requireScope(ScopeAdmin, s.meteredBytes(egress.ClassGit, http.HandlerFunc(s.handleGitcacheGit))))
	mux.Handle("POST /api/v1/gitcache/git/{path...}", requireScope(ScopeAdmin, s.meteredBytes(egress.ClassGit, http.HandlerFunc(s.handleGitcacheGit))))
	mux.Handle("POST /api/v1/cache/store-ceiling/measure", requireScope(ScopeAdmin, s.handleCacheStoreCeiling("measure")))
	mux.Handle("POST /api/v1/cache/store-ceiling/thaw", requireScope(ScopeAdmin, s.handleCacheStoreCeiling("thaw")))
	mux.Handle("POST /api/v1/runs/{id}/gitcache/git/register", requireScope(ScopeNodesClaim,
		s.claimedRunAccess(http.HandlerFunc(s.handleGitcacheRegister))))
	mux.Handle("GET /api/v1/runs/{id}/gitcache/git/{path...}", requireScope(ScopeNodesClaim,
		s.meteredBytes(egress.ClassGit, s.claimedRunAccess(http.HandlerFunc(s.handleGitcacheGit)))))
	mux.Handle("POST /api/v1/runs/{id}/gitcache/git/{path...}", requireScope(ScopeNodesClaim,
		s.meteredBytes(egress.ClassGit, s.claimedRunAccess(http.HandlerFunc(s.handleGitcacheGit)))))

	mux.Handle("POST /api/v1/runs/{id}/cancel", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleCancelRun)))

	mux.Handle("GET /api/v1/trends", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleTrends)))
	mux.Handle("GET /api/v1/agents", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleAgents)))

	mux.Handle("POST /api/v1/runs/{id}/retry", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleRetry)))
	mux.Handle("GET /api/v1/runs/{id}/attempts", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListAttempts)))

	mux.Handle("GET /api/v1/pipelines", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListPipelines)))
	mux.Handle("GET /api/v1/pipelines/{name}/latest", requireScope(ScopeRunsRead, http.HandlerFunc(s.handlePipelineLatest)))
	mux.Handle("GET /api/v1/pipelines/{name}/profile", requireScope(ScopeNodesClaim, http.HandlerFunc(s.handleGetPipelineProfile)))
	// safety: a pin becomes a hard Kubernetes limit for every later run of that pipeline,
	// so it is bound to a live claim on a run of that pipeline, not to the scope alone.
	mux.Handle("PUT /api/v1/pipelines/{name}/profile/pin", requireScope(ScopeRunsState, s.claimedPipeline(http.HandlerFunc(s.handleSetPipelinePin))))
	// safety: an observation moves the same measured price the pin does, so it
	// carries the pin's claim gate rather than the scope alone.
	mux.Handle("POST /api/v1/pipelines/{name}/profile/observations", requireScope(ScopeRunsState, s.claimedPipeline(http.HandlerFunc(s.handleRecordProfileObservation))))
	mux.Handle("POST /api/v1/pipelines/{name}/profile/contention", requireScope(ScopeRunsState, s.claimedPipeline(http.HandlerFunc(s.handleRecordContention))))
	mux.Handle("POST /api/v1/pipelines/{name}/profile/waits", requireScope(ScopeRunsState, s.claimedPipeline(http.HandlerFunc(s.handleRecordWaitObservation))))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/metrics", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleAddNodeMetric)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAddNodeMetric)))))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/metrics", requireScope(ScopeRunsRead, s.readableRun(http.HandlerFunc(s.handleGetNodeMetrics)), ScopeNodesClaim, ScopeTriggersClaim))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/usage", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleAddNodeUsage)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAddNodeUsage)))))

	mux.Handle("DELETE /api/v1/runs/{id}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteRun)))

	mux.Handle("GET /api/v1/crons", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListCrons)))
	mux.Handle("PUT /api/v1/crons/repos", requireScope(ScopeRunsControl, http.HandlerFunc(s.handlePutCronRepo)))
	mux.Handle("DELETE /api/v1/crons/repos", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleDeleteCronRepo)))
	mux.Handle("GET /api/v1/crons/{id}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetCron)))
	mux.Handle("POST /api/v1/crons/{id}/pause", requireScope(ScopeRunsControl, http.HandlerFunc(s.handlePauseCron)))
	mux.Handle("POST /api/v1/crons/{id}/resume", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleResumeCron)))
	mux.Handle("POST /api/v1/crons/{id}/run", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleRunCronNow)))
	mux.Handle("POST /api/v1/crons/{id}/disarm", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleDisarmCron)))
	mux.Handle("PUT /api/v1/crons/{id}/override", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleSetCronOverride)))
	mux.Handle("DELETE /api/v1/crons/{id}/override", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleClearCronOverride)))

	// safety: a slot is a cross-run lock, so the routes that move one bind to the
	// live claim on the run they name rather than to the scope alone. force-release
	// acts on holders from other runs and stays admin.
	mux.Handle("POST /api/v1/concurrency/{key}/acquire", newClaimSensitiveRoute(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotRunFromBody, http.HandlerFunc(s.handleAcquireSlot))).orElse(requireScope(ScopeRunsState, s.claimedSlot(s.slotRunFromBody, http.HandlerFunc(s.handleAcquireSlot)))))
	mux.Handle("POST /api/v1/concurrency/{key}/heartbeat", newClaimReportingRouteBound(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotRunFromBodyHolder, http.HandlerFunc(s.handleHeartbeatSlot))).orElse(requireScope(ScopeRunsState, s.claimedSlot(s.slotRunFromBodyHolder, http.HandlerFunc(s.handleHeartbeatSlot)))))
	mux.Handle("POST /api/v1/concurrency/{key}/release", newClaimReportingRouteBound(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotRunFromBodyHolder, http.HandlerFunc(s.handleReleaseSlot))).orElse(requireScope(ScopeRunsState, s.claimedSlot(s.slotRunFromBodyHolder, http.HandlerFunc(s.handleReleaseSlot)))))
	mux.Handle("GET /api/v1/concurrency/{key}/holder", newClaimReportingRouteBound(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotRunFromQueryHolder, http.HandlerFunc(s.handleObserveSlot))).orElse(requireScope(ScopeRunsState, s.claimedSlot(s.slotRunFromQueryHolder, http.HandlerFunc(s.handleObserveSlot)))))
	mux.Handle("GET /api/v1/concurrency/{key}/state", newClaimReportingRouteBound(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotOwnKey, http.HandlerFunc(s.handleConcurrencyState))).orElse(requireScope(ScopeRunsRead, http.HandlerFunc(s.handleConcurrencyState))))
	mux.Handle("GET /api/v1/queue/state", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleQueueStateView)))
	mux.Handle("GET /api/v1/concurrency/{key}/resolve", newClaimSensitiveRoute(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotRunFromQueryRun, http.HandlerFunc(s.handleResolveWaiter))).orElse(requireScope(ScopeRunsState, s.claimedSlot(s.slotRunFromQueryRun, http.HandlerFunc(s.handleResolveWaiter)))))
	mux.Handle("POST /api/v1/concurrency/{key}/cancel-waiter", newClaimReportingRouteBound(claimWorkKinds, ownClaimBinding, s.claimedSlot(s.slotRunFromBody, http.HandlerFunc(s.handleCancelWaiter))).orElse(requireScope(ScopeRunsState, s.claimedSlot(s.slotRunFromBody, http.HandlerFunc(s.handleCancelWaiter)))))
	mux.Handle("POST /api/v1/concurrency/{key}/force-release", requireScope(ScopeAdmin, http.HandlerFunc(s.handleForceRelease)))

	mux.Handle("GET /api/v1/admin/usage-metrics", requireScope(ScopeAdmin, http.HandlerFunc(s.handleUsageMetrics)))
	// safety: the run counters name every team's pipelines, so on the shared
	// API listener the scrape is the operator's; --metrics-addr serves it open.
	if s.metricsAddr == "" {
		mux.Handle("GET /metrics", requireScope(ScopeAdmin, metricsHandler()))
	}

	mux.Handle("GET /api/v1/object-store/breaker", requireScope(ScopeAdmin, http.HandlerFunc(s.handleObjectStoreBreaker)))
	mux.Handle("POST /api/v1/object-store/reset-breaker", requireScope(ScopeAdmin, http.HandlerFunc(s.handleResetObjectStoreBreaker)))

	mux.Handle("POST /api/v1/nodes/claim", requireScope(ScopeNodesClaim, s.idlePollBudgeted(http.HandlerFunc(s.handleClaimNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/mark-ready", requireScope(ScopeRunsState, s.withTriggerClaimFence(http.HandlerFunc(s.handleMarkNodeReady))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/heartbeat", newClaimReportingRoute(claimSourceKinds, http.HandlerFunc(s.handleClaimHeartbeat)).orElse(requireScope(ScopeNodesClaim, s.heartbeatBudgeted(http.HandlerFunc(s.handleHeartbeatNodeClaim)))))
	// safety: a dispatcher that executes a node itself needs the fence every
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/claim", requireScope(ScopeNodesClaim, s.claimBudgeted(http.HandlerFunc(s.handleClaimNamedNode))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/execution-start", newClaimReportingRoute(claimSourceKinds, http.HandlerFunc(s.handleClaimExecutionStart)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAcknowledgeNodeExecutionStart)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/execution-finish", requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleFinishNodeExecutionAttempt))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/claim/input", newClaimSensitiveRoute(claimWorkKinds, pathClaimBinding, http.HandlerFunc(s.handleClaimInput)))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/activity", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleUpdateNodeActivity)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleUpdateNodeActivity)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/annotations", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleAppendNodeAnnotation)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAppendNodeAnnotation)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/summary", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleSetNodeSummary)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSetNodeSummary)))))

	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/start", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleStartNodeStep)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleStartNodeStep)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/finish", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleFinishNodeStep)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleFinishNodeStep)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/skip", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleSkipNodeStep)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSkipNodeStep)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/annotations", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleAppendStepAnnotation)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleAppendStepAnnotation)))))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/steps/summary", newClaimReportingRoute(claimWorkKinds, http.HandlerFunc(s.handleSetStepSummary)).orElse(requireScope(ScopeNodesClaim, s.claimedBy(http.HandlerFunc(s.handleSetStepSummary)))))
	mux.Handle("GET /api/v1/runs/{id}/steps", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListNodeSteps)))

	mux.Handle("POST /api/v1/runs/{id}/debug-pauses", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateDebugPause)))
	mux.Handle("GET /api/v1/runs/{id}/debug-pauses", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListDebugPauses)))
	mux.Handle("GET /api/v1/runs/{id}/paused", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListDebugPauses)))

	mux.Handle("GET /api/v1/runs/{id}/events", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListEvents)))
	mux.Handle("GET /api/v1/runs/{id}/nodes/{nodeID}/debug-pause", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetActiveDebugPause)))
	mux.Handle("POST /api/v1/runs/{id}/nodes/{nodeID}/release", requireScope(ScopeRunsControl, http.HandlerFunc(s.handleReleaseDebugPause)))

	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}/request", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRequestApproval)))
	mux.Handle("POST /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeApprovalsWrite, http.HandlerFunc(s.handleResolveApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals/{nodeID}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetApproval)))
	mux.Handle("GET /api/v1/runs/{id}/approvals", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListApprovalsForRun)))
	mux.Handle("GET /api/v1/approvals/pending", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListPendingApprovals)))

	mux.Handle("POST /api/v1/tokens", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateToken)))
	mux.Handle("GET /api/v1/tokens", requireScope(ScopeAdmin, http.HandlerFunc(s.handleListTokens)))
	mux.Handle("GET /api/v1/tokens/{prefix}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleLookupTokenByPrefix)))
	mux.Handle("DELETE /api/v1/tokens/{prefix}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRevokeToken)))

	mux.Handle("GET /api/v1/auth/whoami", http.HandlerFunc(s.handleWhoami))

	mux.Handle("GET /api/v1/me", http.HandlerFunc(s.handleMe))
	mux.Handle("DELETE /api/v1/me", http.HandlerFunc(s.handleDeleteMe))
	mux.Handle("GET /api/v1/me/team-deletions", http.HandlerFunc(s.handleMyTeamDeletions))
	mux.Handle("POST /api/v1/me/active-team", http.HandlerFunc(s.handleSetActiveTeam))
	mux.Handle("GET /api/v1/me/identities", http.HandlerFunc(s.handleIdentities))
	mux.Handle("DELETE /api/v1/me/identities/{provider}", http.HandlerFunc(s.handleIdentityUnlink))
	mux.Handle("POST /api/v1/teams", http.HandlerFunc(s.handleCreateTeam))
	mux.Handle("POST /api/v1/invitations/{id}/accept", http.HandlerFunc(s.handleAcceptInvitation))
	mux.Handle("PATCH /api/v1/team", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleRenameTeam)))
	mux.Handle("DELETE /api/v1/team", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleDeleteTeam)))
	mux.Handle("GET /api/v1/team/members", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListMembers)))
	mux.Handle("PATCH /api/v1/team/members/{user_id}", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleSetMemberRole)))
	mux.Handle("DELETE /api/v1/team/members/{user_id}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleRemoveMember)))
	mux.Handle("GET /api/v1/team/invitations", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleListInvitations)))
	mux.Handle("POST /api/v1/team/invitations", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleInvite)))
	mux.Handle("DELETE /api/v1/team/invitations/{id}", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleDeleteInvitation)))
	mux.Handle("GET /api/v1/team/build-trust", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGetBuildTrust)))
	mux.Handle("PUT /api/v1/team/build-trust", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handlePutBuildTrust)))
	mux.Handle("GET /api/v1/team/git-credentials", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListGitCredentials)))
	mux.Handle("POST /api/v1/team/git-credentials", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handlePutGitCredential)))
	mux.Handle("POST /api/v1/team/git-credentials/{host}/confirm", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleConfirmGitCredential)))
	mux.Handle("DELETE /api/v1/team/git-credentials/{host}", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleDeleteGitCredential)))
	mux.Handle("GET /api/v1/team/git-credentials/releases", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleListGitCredentialReleases)))
	mux.Handle("POST /api/v1/team/tokens", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleCreateTeamToken)))
	mux.Handle("GET /api/v1/team/tokens", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListTeamTokens)))
	mux.Handle("DELETE /api/v1/team/tokens/{prefix}", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleRevokeTeamToken)))
	mux.Handle("PUT /api/v1/team/tokens/{prefix}/git-credentials", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleSetTeamTokenGitCredentials)))
	mux.Handle("GET /api/v1/team/github-runners", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListGitHubRunnerBindings)))
	mux.Handle("POST /api/v1/team/github-runners", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleAddGitHubRunnerBinding)))
	mux.Handle("DELETE /api/v1/team/github-runners/{repository_id}", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleRemoveGitHubRunnerBinding)))
	mux.Handle("GET /api/v1/team/github-app", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGitHubAppShow)))
	mux.Handle("DELETE /api/v1/team/github-app/installations/{installation_id}", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleGitHubAppUnbind)))
	mux.Handle("GET /api/v1/team/github-app/installations/{installation_id}/repositories", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGitHubAppRepositories)))
	mux.Handle("GET /api/v1/team/github-app/triggers", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListGitHubAppTriggers)))
	mux.Handle("PUT /api/v1/team/github-app/triggers", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handlePutGitHubAppTrigger)))
	mux.Handle("DELETE /api/v1/team/github-app/triggers", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleDeleteGitHubAppTrigger)))
	mux.Handle("GET /api/v1/team/github-app/automation", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleGitHubAppAutomation)))
	mux.Handle("PUT /api/v1/team/github-app/automation", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleGitHubAppAutomation)))
	mux.Handle("DELETE /api/v1/team/github-app/automation", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleGitHubAppAutomation)))
	mux.Handle("GET /api/v1/team/github-app/extra-repos", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListGitHubAppExtraRepos)))
	mux.Handle("PUT /api/v1/team/github-app/extra-repos", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handlePutGitHubAppExtraRepos)))
	mux.Handle("DELETE /api/v1/github-app/installations/{installation_id}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleOperatorGitHubAppUnbind)))

	// safety: service discovery names the internal cache URL, so any bearer will do but anonymity will not.
	mux.Handle("GET /api/v1/services", refuseRoleless(http.HandlerFunc(s.handleServices)))

	mux.Handle("POST /api/v1/tokens/{prefix}/rotate", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRotateToken)))
	mux.Handle("POST /api/v1/tokens/{prefix}/metered", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetTokenMetered)))

	mux.Handle("GET /api/v1/storage", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleStorageShow)))
	mux.Handle("PUT /api/v1/storage/settings", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetStorageSettings)))
	mux.Handle("PUT /api/v1/storage/quotas/{team}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetStorageQuota)))
	mux.Handle("PUT /api/v1/storage/quotas/{team}/allowance", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetStorageAllowance)))

	mux.Handle("GET /api/v1/team/billing", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleTeamBilling)))
	mux.Handle("POST /api/v1/team/billing/checkout", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleTeamBillingCheckout)))
	mux.Handle("POST /api/v1/team/billing/card", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleTeamBillingCard)))
	mux.Handle("POST /api/v1/team/billing/pay", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleTeamBillingPay)))
	mux.Handle("PUT /api/v1/team/billing/budget", requireScope(ScopeTeamAdmin, http.HandlerFunc(s.handleTeamBillingBudget)))
	mux.Handle("GET /api/v1/credits", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleCreditsShow)))
	mux.Handle("GET /api/v1/credits/history", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleCreditsHistory)))
	mux.Handle("POST /api/v1/credits/grants", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleCreditsGrant)))
	mux.Handle("POST /api/v1/credits/reversals", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleReversePayment)))
	mux.Handle("POST /api/v1/credits/freezes", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleCreditFreeze)))
	mux.Handle("POST /api/v1/credits/cards", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleSavedCard)))
	mux.Handle("POST /api/v1/credits/card-payments", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleCardPayment)))
	mux.Handle("POST /api/v1/credits/warnings", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handlePaymentWarning)))
	mux.Handle("POST /api/v1/credits/card-refunds", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleCardRefund)))
	mux.Handle("GET /api/v1/credits/units", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handleCreditUnits)))
	mux.Handle("GET /api/v1/credits/payments/{reference}", requireServiceScope(ScopeCreditsGrant, http.HandlerFunc(s.handlePaidGrantLookup)))
	mux.Handle("GET /api/v1/credits/settings", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleCreditsSettingsShow)))
	mux.Handle("PUT /api/v1/credits/settings", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreditsSettingsSet)))
	mux.Handle("GET /api/v1/compute-limits", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleComputeLimitsShow)))
	mux.Handle("PUT /api/v1/compute-limits", requireScope(ScopeAdmin, http.HandlerFunc(s.handleComputeLimitsSet)))

	mux.Handle("DELETE /api/v1/accounts/{account}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleOperatorDeleteAccount)))
	mux.Handle("DELETE /api/v1/teams/{team}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleOperatorDeleteTeam)))
	mux.Handle("PUT /api/v1/teams/{team}/repos/{owner}/{name}/dispatch", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetRepoDispatch)))
	mux.Handle("POST /api/v1/launcher/claim", requireScope(ScopeClaimsLaunch, http.HandlerFunc(s.handleLaunchClaim)))
	mux.Handle("POST /api/v1/launcher/sync", requireScope(ScopeClaimsLaunch, http.HandlerFunc(s.handleLauncherSync)))
	mux.Handle("GET /api/v1/operator/teams", s.requireOperator(s.handleOperatorTeams))
	mux.Handle("GET /api/v1/operator/teams/{team}", s.requireOperator(s.handleOperatorTeam))
	mux.Handle("POST /api/v1/operator/teams/{team}/trust", s.requireOperator(s.handleBillingTrustSet))
	mux.Handle("POST /api/v1/operator/teams/{team}/grants", s.requireOperator(s.handleOperatorGrant))
	mux.Handle("POST /api/v1/operator/teams/{team}/freeze", s.requireOperator(s.handleOperatorFreeze))
	mux.Handle("POST /api/v1/operator/teams/{team}/unfreeze", s.requireOperator(s.handleOperatorUnfreeze))
	mux.Handle("GET /api/v1/operator/waitlist", s.requireOperator(s.handleOperatorWaitlist))
	mux.Handle("POST /api/v1/operator/waitlist/approve", s.requireOperator(s.handleApproveWaitlist))
	mux.Handle("GET /api/v1/signups", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSignUpStatus)))
	mux.Handle("PUT /api/v1/signups", requireScope(ScopeAdmin, http.HandlerFunc(s.handleSetSignUp)))

	mux.Handle("POST /api/v1/secrets", requireScope(ScopeAdmin, http.HandlerFunc(s.handleCreateSecret), ScopeTeamAdmin))
	mux.Handle("GET /api/v1/secrets", requireScope(ScopeRunsRead, http.HandlerFunc(s.handleListSecrets), ScopeTeamAdmin))
	mux.Handle("GET /api/v1/secrets/{name}", newClaimSensitiveRoute(claimSecretKinds, secretRunBinding, http.HandlerFunc(s.handleClaimSecret)).orElse(requireScope(ScopeSecretsRead, http.HandlerFunc(s.handleGetSecret), ScopeTeamAdmin)))
	mux.Handle("DELETE /api/v1/secrets/{name}", requireScope(ScopeAdmin, http.HandlerFunc(s.handleDeleteSecret), ScopeTeamAdmin))
	mux.Handle("POST /api/v1/secrets/rotate", requireScope(ScopeAdmin, http.HandlerFunc(s.handleRotateSecrets)))

	router := http.NewServeMux()
	router.HandleFunc("GET /api/v1/health", s.handleHealth)
	// safety: the cache proves itself with its storage.count service token,
	// which these handlers check.
	router.HandleFunc("POST /internal/storage/reserve", s.handleStorageReserve)
	router.HandleFunc("POST /api/v1/data/upload", s.handleDirectUpload)
	router.HandleFunc("POST /api/v1/data/commit", s.handleDirectCommit)
	router.HandleFunc("GET /api/v1/data/capabilities", s.handleDirectCapabilities)
	// safety: these carry a URL signature this process minted, not a bearer.
	router.HandleFunc("PUT /api/v1/outputs/uploads/{id}", s.handleOutputBlobPut)
	router.HandleFunc("GET /api/v1/outputs/objects/{key...}", s.handleOutputBlobGet)
	router.HandleFunc("PUT /api/v1/log-segments/{key...}", s.handleLogSegmentPut)
	router.HandleFunc("POST /internal/storage/commit", s.handleStorageCommit)
	router.HandleFunc("POST /internal/storage/release", s.handleStorageRelease)
	router.HandleFunc("POST /internal/downloads/charge", s.handleDownloadCharge)
	router.Handle("POST /api/v1/data/download", noStore(http.HandlerFunc(s.handleDataDownload)))
	router.Handle("POST /api/v1/auth/logout", http.HandlerFunc(s.handleLogout))
	router.Handle("GET /api/v1/auth/session", noStore(http.HandlerFunc(s.handleSession)))
	router.Handle("GET /api/v1/capabilities", http.HandlerFunc(s.handleCapabilities))
	router.Handle("POST /webhooks/github-app", http.HandlerFunc(s.handleGitHubAppWebhook))
	// safety: the caller proves itself with a GitHub Actions ID token, not a bearer, so this route is public.
	router.Handle("POST /api/v1/runners/github/exchange", noStore(http.HandlerFunc(s.handleGitHubRunnerExchange)))
	router.HandleFunc("GET /.well-known/openid-configuration", s.handleOIDCDiscovery)
	router.HandleFunc("GET /.well-known/jwks.json", s.handleOIDCKeys)

	return mux, router
}

// safety: several routes are registered only when this server was given the
// surface behind them, so the answer is this server's, not the build's. It
// runs registration a second time rather than reading the serving muxes,
// because the caller asks before the store that server needs is open.
func (s *Server) routeProbe() (*http.ServeMux, *http.ServeMux) {
	s.routeProbeOnce.Do(func() {
		s.routeProbeMux, s.routeProbePub = s.routers(s.handleFinishRun)
	})
	return s.routeProbeMux, s.routeProbePub
}

// RegisteredRoute reports whether this server registers a handler for the
// request's method and path. A caller that answers requests in front of a
// controller uses it to tell a route this server does not serve, which is a
// permanent answer, from a condition that may clear.
func (s *Server) RegisteredRoute(r *http.Request) bool {
	mux, router := s.routeProbe()
	return routeRegistered(router, r) || routeRegistered(mux, r)
}

// WriteUnsupportedRoute answers a request for a route this build does not
// register, in the body [UnsupportedRouteError] documents.
func WriteUnsupportedRoute(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error": UnsupportedRouteError,
		"route": r.Method + " " + r.URL.Path,
	})
}

func (s *Server) authenticated(mux *http.ServeMux, next http.Handler) http.Handler {
	byToken := s.authMiddleware().Middleware(next)
	return noStore(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if raw, route := claimRouteFor(mux, r); raw != "" {
			s.serveClaimBearer(w, r, raw, route, next, byToken)
			return
		}
		if sess := browserSessionFrom(r.Context()); sess != nil {
			observeRequestPrincipal(sess.principal.Kind)
			ctx := contextWithPrincipal(r.Context(), sess.principal)
			stampPrincipal(ctx, sess.principal)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		// safety: a session is resolved even while bearer auth is off, because
		// a signed-in account's team and role come only from its session and a
		// request carrying one must not fall through as an anonymous caller.
		if raw := extractSessionHeader(r); raw != "" {
			s.serveSession(w, r, raw, next)
			return
		}
		if s.peerPrincipal == nil || r.Header.Get("Authorization") != "" {
			byToken.ServeHTTP(w, r)
			return
		}
		p := s.peerPrincipal(r)
		if p == nil {
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code:    "unauthenticated",
				Message: "the connection carries no identity this controller accepts",
			})
			return
		}
		observeRequestPrincipal(p.Kind)
		ctx := contextWithPrincipal(r.Context(), p)
		stampPrincipal(ctx, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	}))
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

func unsupportedRouteFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if routeRegistered(mux, r) {
			mux.ServeHTTP(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{
			"error": UnsupportedRouteError,
			"route": r.Method + " " + r.URL.Path,
		})
	})
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
	go s.runStoragePass(ctx)
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

// WithExternalURL declares the base URL this controller answers on from
// outside the cluster. Signed filesystem output URLs use that address.
// Empty uses the address each request arrived at.
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
			s.sweepEgressUsage(ctx)
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
			logRequest(ctx, logger, r, rec, w.Header(), status, elapsed, clientIP(r))
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
