package controller

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// Principal is the request-scoped authenticated identity.
type Principal struct {
	Name        string    // free-form label ("alice", "pool-prod")
	Kind        string    // "user" | "runner" | "service", a team token as the kind its family acts as
	Scopes      []string  // exact-string set membership
	TokenPrefix string    // non-secret prefix for audit
	Authed      time.Time // when this request authenticated
	// Team is the team the request acts for: the token's team, or the
	// session's. Tenant-scoped handlers read it from here and never from the
	// request.
	Team store.Team
	// Role is the account's role in Team, resolved from the membership on
	// this request. Empty for a token, a password session, or an account
	// that is no longer a member of Team.
	Role string
	// AccountID is set when a signed-in account's session authenticated
	// the request.
	AccountID string
	// Expires is when the credential stops authenticating: its expiry, or
	// its scheduled revocation if that comes first. Zero never expires.
	Expires time.Time

	session    string
	signedInAt time.Time
}

// HasScope reports whether the principal carries the named scope.
func (p *Principal) HasScope(s string) bool {
	return slices.Contains(p.Scopes, s)
}

// Scope names used throughout the controller. Centralized as
// constants so a rename is a compile-error not a silent drift.
const (
	ScopeRunsRead = store.ScopeRunsRead
	// ScopeRunsWrite gates starting new work: POST /api/v1/triggers and
	// the Git cache refresh. It does not reach an existing run.
	ScopeRunsWrite = store.ScopeRunsWrite
	// ScopeRunsControl gates acting on a run somebody else started, or on
	// a schedule: retry, cancel, release a debug pause, and the
	// cron writes including firing one now. These are operator actions, so
	// they are held apart from the scopes a runner carries: a runner
	// reports state on work it holds and never reaches for another run.
	ScopeRunsControl  = store.ScopeRunsControl
	ScopeNodesClaim   = store.ScopeNodesClaim
	ScopeLogsRead     = store.ScopeLogsRead
	ScopeTriggersRead = store.ScopeTriggersRead
	// ScopeTriggersClaim gates the trigger worker lifecycle: claim,
	// heartbeat, and done. It carries no authority over run or node
	// state.
	ScopeTriggersClaim = store.ScopeTriggersClaim
	// ScopeRunsState gates run and node state writes: run create and
	// finish, node create, start, finish, and run event append. The
	// per-node routes are additionally bound to the caller's own
	// claim.
	ScopeRunsState = store.ScopeRunsState
	// ScopeSecretsRead releases no secret. A node reads its run's secrets
	// with its claim token, so a bearer holding only this scope is answered
	// 403 claim_required on GET /api/v1/secrets/{name}. Tokens minted with
	// it keep authenticating; no team token or runner is minted with it.
	ScopeSecretsRead = store.ScopeSecretsRead
	// ScopeApprovalsWrite gates POST /api/v1/runs/{run}/approvals/{node}.
	// Any principal with this scope can resolve any approval. Reads
	// are covered by runs.read.
	ScopeApprovalsWrite = store.ScopeApprovalsWrite
	// ScopeTeamAdmin gates administering the principal's own team: its
	// members, invitations, roles, name, and every runner token it holds.
	// A team owner holds it. It reaches no other team and no deployment
	// setting, which is what ScopeAdmin is for.
	ScopeTeamAdmin = store.ScopeTeamAdmin
	// ScopeAdmin is the deployment operator's scope. No team membership
	// grants it.
	ScopeAdmin = "admin"
	// ScopeClaimsLaunch is the launcher's scope. It claims any team's ready
	// node of a controller-dispatched run, and the claim's token, for the Job
	// the launcher builds, and it marks that claim as Cloud compute. It reads
	// no secret or source. Only the operator mints it; no team's token may
	// carry it.
	ScopeClaimsLaunch = store.LaunchScope
)

const authUnavailableRetryAfter = 5 * time.Second

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
	TokenState   string `json:"token_state,omitempty"`
	Message      string `json:"message"`
}

func writeAuthError(w http.ResponseWriter, status int, body authErrorBody) {
	writeJSON(w, status, body)
}

type principalCtxKey struct{}

func contextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	noteAuditPrincipal(ctx, p)
	return context.WithValue(ctx, principalCtxKey{}, p)
}

// PrincipalFromContext returns the principal stamped by the auth
// middleware, or (nil, false) when auth is disabled or the request
// preceded the middleware.
func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalCtxKey{}).(*Principal)
	return p, ok
}
