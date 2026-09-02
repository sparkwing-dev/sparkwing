package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/otelutil"
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
	store    *store.Store
	cache    sync.Map
	cacheTTL time.Duration
	negCache sync.Map
	negCount atomic.Int64
	negTTL   time.Duration
	now      func() time.Time
}

type authCacheEntry struct {
	principal *Principal
	expires   time.Time
}

type authFailureEntry struct {
	reason  string
	expires time.Time
}

const (
	negativeAuthCacheTTL = 5 * time.Second
	negativeAuthCacheCap = 4096
)

// NewAuthenticator constructs an Authenticator over the given store.
// Pass cacheTTL=0 to disable caching.
func NewAuthenticator(st *store.Store, cacheTTL time.Duration) *Authenticator {
	negTTL := time.Duration(0)
	if cacheTTL > 0 {
		negTTL = min(negativeAuthCacheTTL, cacheTTL)
	}
	return &Authenticator{
		store:    st,
		cacheTTL: cacheTTL,
		negTTL:   negTTL,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// Authenticate resolves a raw bearer token to a Principal or an
// error. Returned errors are safe to surface to the caller as a 401
// body; they never contain the token itself or the stored hash.
func (a *Authenticator) Authenticate(raw string) (*Principal, error) {
	if raw == "" {
		return nil, errors.New("missing bearer token")
	}
	now := a.now()

	if a.cacheTTL > 0 {
		if v, ok := a.cache.Load(raw); ok {
			e := v.(*authCacheEntry)
			if now.Before(e.expires) {
				cp := *e.principal
				cp.Authed = now
				return &cp, nil
			}
			a.cache.Delete(raw)
		}
	}

	if store.TokenKindFromPrefix(raw) == "" {
		return nil, errors.New("invalid bearer token")
	}
	// safety: a replayed wrong guess answers from this cache, so one raw token costs at most one argon2 verification.
	if reason, ok := a.recentFailure(raw, now); ok {
		return nil, errors.New(reason)
	}
	tok, err := a.store.LookupToken(raw, now)
	if err != nil {
		a.rememberFailure(raw, err, now)
		return nil, err
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

	if a.cacheTTL > 0 {
		a.cache.Store(raw, &authCacheEntry{
			principal: principal,
			expires:   now.Add(a.cacheTTL),
		})
	}
	return principal, nil
}

func (a *Authenticator) recentFailure(raw string, now time.Time) (string, bool) {
	if a.negTTL <= 0 {
		return "", false
	}
	v, ok := a.negCache.Load(raw)
	if !ok {
		return "", false
	}
	e := v.(*authFailureEntry)
	if !now.Before(e.expires) {
		a.forgetFailure(raw)
		return "", false
	}
	return e.reason, true
}

func (a *Authenticator) rememberFailure(raw string, reason error, now time.Time) {
	if a.negTTL <= 0 {
		return
	}
	if a.negCount.Load() >= negativeAuthCacheCap {
		a.sweepFailures(now)
		if a.negCount.Load() >= negativeAuthCacheCap {
			return
		}
	}
	entry := &authFailureEntry{reason: reason.Error(), expires: now.Add(a.negTTL)}
	if _, loaded := a.negCache.Swap(raw, entry); !loaded {
		a.negCount.Add(1)
	}
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
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code:    "unauthenticated",
				Message: err.Error(),
			})
			return
		}
		p, err := a.Authenticate(raw)
		if err != nil {
			writeAuthError(w, http.StatusUnauthorized, authErrorBody{
				Code:    "unauthenticated",
				Message: err.Error(),
			})
			return
		}
		ctx := contextWithPrincipal(r.Context(), p)
		otelutil.StampSpan(ctx, otelutil.SpanAttrs{Principal: p.Name})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix)), nil
}

func requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		if p.HasScope(ScopeAdmin) || p.HasScope(scope) {
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

func principalName(r *http.Request) string {
	p, ok := PrincipalFromContext(r.Context())
	if !ok {
		return ""
	}
	return p.Name
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
		return []slog.Attr{slog.String("principal", "unauthed")}
	}
	return []slog.Attr{
		slog.String("principal", p.Name),
		slog.String("kind", p.Kind),
		slog.String("token_prefix", p.TokenPrefix),
	}
}
