package web

import (
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type proxyRoute struct {
	pattern string
	scope   string
}

// safety: only routes the dashboard calls are forwarded, so its bearer cannot be borrowed for other routes.
var proxyRoutes = []proxyRoute{
	{"GET /api/v1/runs", store.ScopeRunsRead},
	{"GET /api/v1/runs/{id}", store.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/attempts", store.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/events", store.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/paused", store.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/approvals/{nodeID}", store.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/nodes/{nodeID}/metrics", store.ScopeRunsRead},
	{"GET /api/v1/approvals/pending", store.ScopeRunsRead},
	{"GET /api/v1/agents", store.ScopeRunsRead},
	{"GET /api/v1/queue/state", store.ScopeRunsRead},
	{"GET /api/v1/trends", store.ScopeRunsRead},
	{"GET /api/v1/pipelines", store.ScopeRunsRead},
	{"POST /api/v1/triggers", store.ScopeRunsWrite},
	{"POST /api/v1/runs/{id}/cancel", store.ScopeRunsControl},
	{"POST /api/v1/runs/{id}/retry", store.ScopeRunsControl},
	{"POST /api/v1/runs/{id}/nodes/{nodeID}/release", store.ScopeRunsControl},
	{"POST /api/v1/runs/{id}/approvals/{nodeID}", store.ScopeApprovalsWrite},
	{"GET /api/v1/crons", store.ScopeRunsRead},
	{"GET /api/v1/crons/{id}", store.ScopeRunsRead},
	{"PUT /api/v1/crons/repos", store.ScopeRunsControl},
	{"DELETE /api/v1/crons/repos", store.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/pause", store.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/resume", store.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/run", store.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/disarm", store.ScopeRunsControl},
	{"PUT /api/v1/crons/{id}/override", store.ScopeRunsControl},
	{"DELETE /api/v1/crons/{id}/override", store.ScopeRunsControl},
	{"DELETE /api/v1/runs/{id}", store.OperatorScope},
	{"GET /api/v1/team/github-app", store.ScopeRunsRead},
	{"DELETE /api/v1/team/github-app/installations/{installation_id}", store.ScopeTeamAdmin},
	{"GET /api/v1/team/github-app/installations/{installation_id}/repositories", store.ScopeRunsRead},
	{"GET /api/v1/team/github-app/triggers", store.ScopeRunsRead},
	{"PUT /api/v1/team/github-app/triggers", store.ScopeTeamAdmin},
	{"DELETE /api/v1/team/github-app/triggers", store.ScopeTeamAdmin},
	{"GET /api/v1/team/github-app/extra-repos", store.ScopeRunsRead},
	{"PUT /api/v1/team/github-app/extra-repos", store.ScopeTeamAdmin},
	{"GET /api/v1/team/billing", store.ScopeRunsRead},
	{"POST /api/v1/team/billing/checkout", store.ScopeTeamAdmin},
	{"GET /api/v1/team/git-credentials", store.ScopeRunsRead},
	{"POST /api/v1/team/git-credentials", store.ScopeTeamAdmin},
	{"POST /api/v1/team/git-credentials/{host}/confirm", store.ScopeTeamAdmin},
	{"DELETE /api/v1/team/git-credentials/{host}", store.ScopeTeamAdmin},
	{"GET /api/v1/team/git-credentials/releases", store.ScopeTeamAdmin},
	{"PUT /api/v1/team/runner-tokens/{prefix}/git-credentials", store.ScopeTeamAdmin},
	// safety: no single-secret read is proxied, so a browser never reaches a
	// value route; the list returns variable values and no secret's.
	{"GET /api/v1/secrets", store.ScopeRunsRead},
	{"POST /api/v1/secrets", store.ScopeTeamAdmin},
	{"DELETE /api/v1/secrets/{name}", store.ScopeTeamAdmin},
}

// safety: a membership role, which the controller resolves on every request for the
// session's active team, decides these routes, so the dashboard adds no scope of its own.
var identityProxyRoutes = []proxyRoute{
	{"GET /api/v1/me", ""},
	{"DELETE /api/v1/me", ""},
	{"GET /api/v1/me/team-deletions", ""},
	{"POST /api/v1/me/active-team", ""},
	{"GET /api/v1/me/identities", ""},
	{"DELETE /api/v1/me/identities/{provider}", ""},
	{"POST /api/v1/teams", ""},
	{"PATCH /api/v1/team", ""},
	{"DELETE /api/v1/team", ""},
	{"GET /api/v1/team/members", ""},
	{"PATCH /api/v1/team/members/{userID}", ""},
	{"DELETE /api/v1/team/members/{userID}", ""},
	{"GET /api/v1/team/invitations", ""},
	{"POST /api/v1/team/invitations", ""},
	{"DELETE /api/v1/team/invitations/{id}", ""},
	{"POST /api/v1/invitations/{id}/accept", ""},
	{"GET /api/v1/team/cli-tokens", ""},
	{"POST /api/v1/team/cli-tokens", ""},
	{"DELETE /api/v1/team/cli-tokens/{prefix}", ""},
	{"GET /api/v1/team/runner-tokens", ""},
	{"POST /api/v1/team/runner-tokens", ""},
	{"DELETE /api/v1/team/runner-tokens/{prefix}", ""},
}

// safety: the controller answers these only for a listed operator account's
// session, and the dashboard's own bearer never reaches them.
var operatorProxyRoutes = []proxyRoute{
	{"GET /api/v1/operator/session", ""},
	{"GET /api/v1/operator/teams", ""},
	{"GET /api/v1/operator/teams/{team}", ""},
	{"POST /api/v1/operator/teams/{team}/trust", ""},
	{"POST /api/v1/operator/teams/{team}/grants", ""},
	{"POST /api/v1/operator/teams/{team}/freeze", ""},
	{"POST /api/v1/operator/teams/{team}/unfreeze", ""},
	{"GET /api/v1/operator/waitlist", ""},
	{"POST /api/v1/operator/waitlist/approve", ""},
}

// safety: the dashboard reads logs on behalf of a browser session, so the logs bearer
// never carries a delete or an append off the browser-facing listener.
var logsProxyRoutes = []proxyRoute{
	{"GET /api/v1/logs/search", store.ScopeLogsRead},
	{"GET /api/v1/logs/{runID}", store.ScopeLogsRead},
	{"GET /api/v1/logs/{runID}/{nodeID}", store.ScopeLogsRead},
	{"GET /api/v1/logs/{runID}/{nodeID}/stream", store.ScopeLogsRead},
}

func proxyAllowList(proxy http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, route := range slices.Concat(proxyRoutes, identityProxyRoutes, operatorProxyRoutes) {
		mux.Handle(route.pattern, requireSessionScope(route.scope, proxy))
	}
	mux.HandleFunc("/api/v1/", routeNotProxied)
	return mux
}

func logsProxyAllowList(proxy http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, route := range logsProxyRoutes {
		mux.Handle(route.pattern, requireSessionScope(route.scope, proxy))
	}
	mux.HandleFunc("/api/v1/logs/", routeNotProxied)
	return mux
}

func routeNotProxied(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusNotFound, map[string]string{
		"error":   "not_proxied",
		"message": "the dashboard does not proxy this controller route",
	})
}

// safety: without a session the dashboard adds no credential of its own, so the controller stays the authority.
func requireSessionScope(scope string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := WebPrincipalFromContext(r.Context())
		if !ok || scope == "" {
			next.ServeHTTP(w, r)
			return
		}
		if slices.Contains(principal.Scopes, store.OperatorScope) ||
			slices.Contains(principal.Scopes, scope) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error":         "missing_scope",
			"missing_scope": scope,
			"principal":     principal.Name,
			"message":       "session lacks required scope: " + scope,
		})
	})
}
