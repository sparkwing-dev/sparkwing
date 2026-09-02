package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/authwire"
	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
	"github.com/sparkwing-dev/sparkwing/internal/ratelimit"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Principal is the request-scoped authenticated identity.
type Principal struct {
	Name        string    // free-form label ("alice", "pool-prod")
	Kind        string    // "user" | "runner" | "service"
	Scopes      []string  // exact-string set membership
	TokenPrefix string    // non-secret prefix for audit
	Authed      time.Time // when this request authenticated
}

// HasScope reports whether the principal carries the named scope.
func (p *Principal) HasScope(s string) bool {
	return slices.Contains(p.Scopes, s)
}

// Scope names used throughout the controller. Centralized as
// constants so a rename is a compile-error not a silent drift.
const (
	ScopeRunsRead     = "runs.read"
	ScopeRunsWrite    = "runs.write"
	ScopeNodesClaim   = "nodes.claim"
	ScopeLogsRead     = "logs.read"
	ScopeLogsWrite    = "logs.write"
	ScopeTriggersRead = "triggers.read"
	// ScopeTriggersClaim gates the trigger worker lifecycle: claim,
	// heartbeat, and done. It carries no authority over run or node
	// state.
	ScopeTriggersClaim = "triggers.claim"
	// ScopeRunsState gates run and node state writes: run create and
	// finish, node create, start, finish, and run event append. The
	// per-node routes are additionally bound to the caller's own
	// claim.
	ScopeRunsState = "runs.state"
	// ScopeSecretsRead gates GET /api/v1/secrets/{name}. A non-admin
	// holder resolves a name against the repository of the run it
	// currently holds a claim in.
	ScopeSecretsRead = "secrets.read"
	// ScopeApprovalsWrite gates POST /api/v1/runs/{run}/approvals/{node}.
	// Any principal with this scope can resolve any approval. Reads
	// are covered by runs.read.
	ScopeApprovalsWrite = "approvals.write"
	ScopeAdmin          = "admin"
)

var allScopes = []string{
	ScopeRunsRead,
	ScopeRunsWrite,
	ScopeNodesClaim,
	ScopeLogsRead,
	ScopeLogsWrite,
	ScopeTriggersRead,
	ScopeTriggersClaim,
	ScopeRunsState,
	ScopeSecretsRead,
	ScopeApprovalsWrite,
	ScopeAdmin,
}

func validateScopes(scopes []string) error {
	var unknown []string
	for _, s := range scopes {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" || slices.Contains(allScopes, trimmed) || slices.Contains(unknown, s) {
			continue
		}
		unknown = append(unknown, s)
	}
	if len(unknown) == 0 {
		return nil
	}
	noun := "scope"
	if len(unknown) > 1 {
		noun = "scopes"
	}
	return fmt.Errorf(
		"unknown %s %s (valid: %s)",
		noun, quoteScopes(unknown), strings.Join(allScopes, ", "),
	)
}

func quoteScopes(scopes []string) string {
	out := make([]string, 0, len(scopes))
	for _, s := range scopes {
		out = append(out, strconv.Quote(s))
	}
	return strings.Join(out, ", ")
}

// Authenticator converts a raw bearer token into a Principal. Hot
// path: prefix-segment lookup in the tokens table (indexed) -> argon2
// verify only on matched rows. An in-memory cache keeps repeated
// lookups cheap.
type Authenticator struct {
	store       *store.Store
	cache       sync.Map
	cacheTTL    time.Duration
	generations sync.Map
	negCache    sync.Map
	negCount    atomic.Int64
	prefixes    *ratelimit.Limiter
	trusted     []netip.Prefix
	logger      *slog.Logger
	now         func() time.Time
	afterLookup func()
}

type authCacheEntry struct {
	principal *Principal
	expires   time.Time
	tokenExp  *time.Time
	revokedAt *time.Time
}

func (e *authCacheEntry) tokenLive(now time.Time) bool {
	if e.revokedAt != nil && !now.Before(*e.revokedAt) {
		return false
	}
	if e.tokenExp != nil && !now.Before(*e.tokenExp) {
		return false
	}
	return true
}

type authFailureEntry struct {
	reason  error
	expires time.Time
}

const (
	negativeAuthCacheTTL    = 5 * time.Second
	negativeAuthCacheCap    = 4096
	negativeAuthCacheTarget = negativeAuthCacheCap * 7 / 8

	// safety: a token prefix is public, so only a per-prefix budget stops a guesser that varies the secret half.
	authPrefixFailureBurst  = 10
	authPrefixFailureWindow = time.Minute

	authBusyRetryAfter        = time.Second
	authUnavailableRetryAfter = 5 * time.Second
)

var (
	errMissingBearer = errors.New("missing bearer token")
	errInvalidBearer = errors.New("invalid bearer token")
	errAuthThrottled = errors.New("too many failed authentication attempts for this token prefix")
)

// NewAuthenticator constructs an Authenticator over the given store.
// Pass cacheTTL=0 to disable caching of successful lookups; the
// failure budgets that bound unauthenticated hashing stay on either
// way.
func NewAuthenticator(st *store.Store, cacheTTL time.Duration) *Authenticator {
	return &Authenticator{
		store:    st,
		cacheTTL: cacheTTL,
		prefixes: ratelimit.New(authPrefixFailureBurst, authPrefixFailureWindow),
		logger:   slog.Default(),
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// WithTrustedProxyCIDRs names the proxy source networks allowed to
// supply X-Forwarded-For when the bearer failure budget resolves a
// caller's address. Empty keys it on the TCP peer.
func (a *Authenticator) WithTrustedProxyCIDRs(prefixes []netip.Prefix) *Authenticator {
	a.trusted = prefixes
	return a
}

// WithLogger routes the detail of failures that are not authentication
// rejections to the given logger. The caller sees only a generic
// message.
func (a *Authenticator) WithLogger(l *slog.Logger) *Authenticator {
	if l != nil {
		a.logger = l
	}
	return a
}

// Authenticate resolves a raw bearer token to a Principal or an
// error. Returned errors are safe to surface to the caller as a 401
// body; they never contain the token itself or the stored hash.
func (a *Authenticator) Authenticate(raw string) (*Principal, error) {
	return a.authenticate(raw, "")
}

func (a *Authenticator) authenticate(raw, client string) (*Principal, error) {
	if raw == "" {
		return nil, errMissingBearer
	}
	now := a.now()

	if a.cacheTTL > 0 {
		if v, ok := a.cache.Load(raw); ok {
			e := v.(*authCacheEntry)
			switch {
			case !now.Before(e.expires):
				a.cache.Delete(raw)
			// safety: a cached entry outlives the row's own clock, so expiry and revocation are rechecked on every hit.
			case !e.tokenLive(now):
				a.cache.Delete(raw)
				return nil, store.ErrTokenRevoked
			default:
				cp := *e.principal
				cp.Authed = now
				return &cp, nil
			}
		}
	}

	if store.TokenKindFromPrefix(raw) == "" || len(raw) < store.PrefixLen {
		return nil, errInvalidBearer
	}
	// safety: a replayed wrong guess answers from this cache, so one raw token costs at most one argon2 verification.
	if reason, ok := a.recentFailure(raw, now); ok {
		return nil, reason
	}
	// safety: a guesser varying the secret half never repeats a raw token, so only this budget bounds its hashing.
	budget := failureKey(raw[:store.PrefixLen], client)
	if !a.prefixes.Peek(budget, now) {
		return nil, errAuthThrottled
	}
	prefix := tokenPrefixOf(raw)
	gen := a.generation(prefix)
	tok, err := a.store.LookupToken(raw, now)
	if err != nil {
		if errors.Is(err, store.ErrUnknownToken) {
			a.prefixes.Penalize(budget, now)
		}
		if authRejection(err) {
			a.rememberFailure(raw, err, now)
		}
		return nil, err
	}
	if a.afterLookup != nil {
		a.afterLookup()
	}
	if tok.RevokedAt != nil && tok.ReplacedBy != "" {
		slog.Warn(
			"token.rotating",
			"prefix", tok.Prefix,
			"principal", tok.Principal,
			"replaced_by", tok.ReplacedBy,
			"revokes_at", tok.RevokedAt.Unix(),
		)
	}
	principal := &Principal{
		Name:        tok.Principal,
		Kind:        tok.Kind,
		Scopes:      tok.Scopes,
		TokenPrefix: tok.Prefix,
		Authed:      now,
	}

	// safety: an Invalidate that landed during this read must win, or the revoked row is re-cached for a full TTL.
	if a.cacheTTL > 0 && a.generation(prefix) == gen {
		a.cache.Store(raw, &authCacheEntry{
			principal: principal,
			expires:   now.Add(a.cacheTTL),
			tokenExp:  tok.ExpiresAt,
			revokedAt: tok.RevokedAt,
		})
	}
	return principal, nil
}

// safety: only "this credential does not authenticate" is safe to cache and safe to echo; a fault is neither.
func authRejection(err error) bool {
	switch {
	case errors.Is(err, errMissingBearer), errors.Is(err, errInvalidBearer):
		return true
	case errors.Is(err, store.ErrInvalidToken), errors.Is(err, store.ErrNoTokenCandidates):
		return true
	case errors.Is(err, store.ErrUnknownToken), errors.Is(err, store.ErrTokenRevoked):
		return true
	case errors.Is(err, store.ErrInvalidCredentials):
		return true
	default:
		return false
	}
}

// Invalidate drops every cached authentication for a token prefix, so
// the next request carrying that token re-reads the row instead of
// answering from a stale cache entry. Revocation and rotation call it.
// Safe on a nil Authenticator.
func (a *Authenticator) Invalidate(prefix string) {
	if a == nil || prefix == "" {
		return
	}
	a.bumpGeneration(prefix)
	a.cache.Range(func(k, v any) bool {
		e, ok := v.(*authCacheEntry)
		if ok && e.principal != nil && e.principal.TokenPrefix == prefix {
			a.cache.Delete(k)
		}
		return true
	})
}

func tokenPrefixOf(raw string) string {
	if len(raw) < store.PrefixLen {
		return raw
	}
	return raw[:store.PrefixLen]
}

func (a *Authenticator) generation(prefix string) uint64 {
	v, ok := a.generations.Load(prefix)
	if !ok {
		return 0
	}
	return v.(*atomic.Uint64).Load()
}

// safety: only Invalidate creates a cell, so unauthenticated traffic carrying invented prefixes cannot grow this map.
func (a *Authenticator) bumpGeneration(prefix string) {
	v, _ := a.generations.LoadOrStore(prefix, new(atomic.Uint64))
	v.(*atomic.Uint64).Add(1)
}

func (a *Authenticator) recentFailure(raw string, now time.Time) (error, bool) {
	v, ok := a.negCache.Load(raw)
	if !ok {
		return nil, false
	}
	e := v.(*authFailureEntry)
	if !now.Before(e.expires) {
		a.forgetFailure(raw)
		return nil, false
	}
	return e.reason, true
}

func (a *Authenticator) rememberFailure(raw string, reason error, now time.Time) {
	// safety: a store or capacity error is transient, so caching it would answer 401 for a valid token after recovery.
	if !authRejection(reason) {
		return
	}
	if a.negCount.Load() >= negativeAuthCacheCap {
		a.evictFailures(now)
	}
	entry := &authFailureEntry{reason: reason, expires: now.Add(negativeAuthCacheTTL)}
	if _, loaded := a.negCache.Swap(raw, entry); !loaded {
		a.negCount.Add(1)
	}
}

// safety: refusing new entries at the cap would let cheap failures pin the cache and restore a hash per replayed guess.
func (a *Authenticator) evictFailures(now time.Time) {
	a.sweepFailures(now)
	a.negCache.Range(func(k, _ any) bool {
		if a.negCount.Load() < negativeAuthCacheTarget {
			return false
		}
		a.forgetFailure(k.(string))
		return true
	})
}

func (a *Authenticator) forgetFailure(raw string) {
	if _, loaded := a.negCache.LoadAndDelete(raw); loaded {
		a.negCount.Add(-1)
	}
}

func (a *Authenticator) sweepFailures(now time.Time) {
	a.negCache.Range(func(k, v any) bool {
		if !now.Before(v.(*authFailureEntry).expires) {
			a.forgetFailure(k.(string))
		}
		return true
	})
}

// AuthDisabled reports whether the Authenticator has no backing token
// store, in which case every request should be allowed through. An
// empty tokens table means auth is off until a token is minted and
// the controller restarts.
func (a *Authenticator) AuthDisabled() bool {
	if a == nil {
		return true
	}
	return a.store == nil
}

// Middleware returns an http.Handler wrapper that authenticates every
// incoming request and stamps the Principal on r.Context(). When the
// Authenticator is disabled (laptop-local, no tokens configured), the
// middleware is a pure pass-through.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	if a.AuthDisabled() {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := extractBearer(r)
		if err != nil {
			a.writeAuthFailure(w, err)
			return
		}
		p, err := a.authenticate(raw, ratelimit.ClientIP(r, a.trusted))
		if err != nil {
			a.writeAuthFailure(w, err)
			return
		}
		ctx := contextWithPrincipal(r.Context(), p)
		otelutil.StampSpan(ctx, otelutil.SpanAttrs{Principal: p.Name})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// safety: a rejection carries its reason; a controller fault answers a generic 503 and logs the detail instead.
func (a *Authenticator) writeAuthFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAuthThrottled):
		setRetryAfter(w, authPrefixFailureWindow)
		writeAuthError(w, http.StatusTooManyRequests, authErrorBody{
			Code:    "too_many_attempts",
			Message: err.Error(),
		})
	case errors.Is(err, store.ErrHashingBusy):
		setRetryAfter(w, authBusyRetryAfter)
		writeAuthError(w, http.StatusServiceUnavailable, authErrorBody{
			Code:    "unavailable",
			Message: "authentication is busy, retry shortly",
		})
	case authRejection(err):
		writeAuthError(w, http.StatusUnauthorized, authErrorBody{
			Code:    "unauthenticated",
			Message: err.Error(),
		})
	default:
		a.log().Error("auth.unavailable", "error", err.Error())
		setRetryAfter(w, authUnavailableRetryAfter)
		writeAuthError(w, http.StatusServiceUnavailable, authErrorBody{
			Code:    "unavailable",
			Message: "authentication is temporarily unavailable",
		})
	}
}

func (a *Authenticator) log() *slog.Logger {
	if a.logger == nil {
		return slog.Default()
	}
	return a.logger
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errMissingBearer
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix)), nil
}

func requireScope(scope string, next http.Handler, alternatives ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		allowed := p.HasScope(ScopeAdmin) || p.HasScope(scope)
		for _, alternative := range alternatives {
			allowed = allowed || p.HasScope(alternative)
		}
		if allowed {
			next.ServeHTTP(w, r)
			return
		}
		writeAuthError(w, http.StatusForbidden, authErrorBody{
			Code:         "missing_scope",
			MissingScope: scope,
			Principal:    p.label(),
			Message:      "token lacks required scope: " + scope,
		})
	})
}

func claimIdentity(r *http.Request) store.ClaimIdentity {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		return store.ClaimIdentity{}
	}
	// safety: the token prefix, not the shared principal label, is what binds a claim.
	return store.ClaimIdentity{Principal: p.Name, TokenPrefix: p.TokenPrefix}
}

func (p *Principal) label() string {
	if p == nil {
		return ""
	}
	if p.Kind == "" {
		return p.Name
	}
	return p.Kind + ":" + p.Name
}

type authErrorBody struct {
	Code         string `json:"error"`
	MissingScope string `json:"missing_scope,omitempty"`
	Principal    string `json:"principal,omitempty"`
	Message      string `json:"message"`
}

func writeAuthError(w http.ResponseWriter, status int, body authErrorBody) {
	writeJSON(w, status, body)
}

type principalCtxKey struct{}

func contextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext returns the principal stamped by the auth
// middleware, or (nil, false) when auth is disabled or the request
// preceded the middleware.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(*Principal)
	return p, ok
}

// AuditFields returns slog.Attrs for the principal for structured
// access logs.
func AuditFields(ctx context.Context) []slog.Attr {
	p, ok := PrincipalFromContext(ctx)
	if !ok {
		return []slog.Attr{slog.String("principal", authwire.AnonymousPrincipal)}
	}
	return []slog.Attr{
		slog.String("principal", p.Name),
		slog.String("kind", p.Kind),
		slog.String("token_prefix", p.TokenPrefix),
	}
}
