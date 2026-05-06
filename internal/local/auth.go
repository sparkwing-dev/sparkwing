package local

// Auth middleware. Flow: extract `Authorization: Bearer X`, look it up
// in the tokens table (prefix index + argon2 verify), stamp the
// principal on ctx. Handlers gate themselves with requireScope; the
// middleware only authenticates.

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/otelutil"
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
	// ScopeApprovalsWrite gates POST /api/v1/runs/{run}/approvals/{node}.
	// Any principal with this scope can resolve any approval. Reads
	// are covered by runs.read.
	ScopeApprovalsWrite = "approvals.write"
	ScopeAdmin          = "admin"
)

// Authenticator converts a raw bearer token into a Principal. Hot
// path: prefix-segment lookup in the tokens table (indexed) -> argon2
// verify only on matched rows. An in-memory cache keeps repeated
// lookups cheap.
type Authenticator struct {
	store    *store.Store
	cache    sync.Map // map[string]*authCacheEntry
	cacheTTL time.Duration
	now      func() time.Time
}

type authCacheEntry struct {
	principal *Principal
	expires   time.Time
}

// NewAuthenticator constructs an Authenticator over the given store.
// Pass cacheTTL=0 to disable caching.
func NewAuthenticator(st *store.Store, cacheTTL time.Duration) *Authenticator {
	return &Authenticator{
		store:    st,
		cacheTTL: cacheTTL,
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
				// Copy so callers mutating the principal don't affect
				// the cached entry.
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
	tok, err := a.store.LookupToken(raw, now)
	if err != nil {
		return nil, err
	}
	// Rotation-grace telemetry: token replaced but still in grace
	// window. Helps operators identify callers that need to swap.
	if tok.RevokedAt != nil && tok.ReplacedBy != "" {
		slog.Warn("token.rotating",
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

// extractBearer pulls the token out of the Authorization header.
// Returns a sanitizable error (no token content leaks).
func extractBearer(r *http.Request) (string, error) {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", errors.New("missing bearer token")
	}
	return strings.TrimSpace(strings.TrimPrefix(h, prefix)), nil
}

// requireScope wraps a handler so it only runs when the request-
// context principal carries the named scope. The `admin` scope is an
// implicit superset. When the Authenticator is disabled, requireScope
// short-circuits to pass-through.
func requireScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFromContext(r.Context())
		if !ok {
			// Auth disabled -- pass through unconditionally.
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

// label renders the principal as "<kind>:<name>" for the IMP-022
// auth-error response body.
func (p *Principal) label() string {
	if p == nil {
		return ""
	}
	if p.Kind == "" {
		return p.Name
	}
	return p.Kind + ":" + p.Name
}

// authErrorBody is the IMP-022 wire shape for 401/403 responses
// emitted by the laptop-local controller's auth middleware. Same
// shape as logs.AuthErrorBody and pkg/controller's authErrorBody.
type authErrorBody struct {
	Code         string `json:"error"`
	MissingScope string `json:"missing_scope,omitempty"`
	Principal    string `json:"principal,omitempty"`
	Message      string `json:"message"`
}

func writeAuthError(w http.ResponseWriter, status int, body authErrorBody) {
	writeJSON(w, status, body)
}

// --- principal ctx helpers ---

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
