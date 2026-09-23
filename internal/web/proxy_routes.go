package web

import (
	"net/http"
	"slices"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

type proxyRoute struct {
	pattern string
	scope   string
}

// safety: only routes the dashboard calls are forwarded, so its bearer cannot be borrowed for other routes.
var proxyRoutes = []proxyRoute{
	{"GET /api/v1/runs", controller.ScopeRunsRead},
	{"GET /api/v1/runs/{id}", controller.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/attempts", controller.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/events", controller.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/paused", controller.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/approvals/{nodeID}", controller.ScopeRunsRead},
	{"GET /api/v1/runs/{id}/nodes/{nodeID}/metrics", controller.ScopeRunsRead},
	{"GET /api/v1/approvals/pending", controller.ScopeRunsRead},
	{"GET /api/v1/agents", controller.ScopeRunsRead},
	{"GET /api/v1/trends", controller.ScopeRunsRead},
	{"POST /api/v1/triggers", controller.ScopeRunsWrite},
	{"POST /api/v1/runs/{id}/cancel", controller.ScopeRunsControl},
	{"POST /api/v1/runs/{id}/retry", controller.ScopeRunsControl},
	{"POST /api/v1/runs/{id}/nodes/{nodeID}/release", controller.ScopeRunsControl},
	{"POST /api/v1/runs/{id}/approvals/{nodeID}", controller.ScopeApprovalsWrite},
	{"GET /api/v1/crons", controller.ScopeRunsRead},
	{"GET /api/v1/crons/{id}", controller.ScopeRunsRead},
	{"PUT /api/v1/crons/repos", controller.ScopeRunsControl},
	{"DELETE /api/v1/crons/repos", controller.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/pause", controller.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/resume", controller.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/run", controller.ScopeRunsControl},
	{"POST /api/v1/crons/{id}/disarm", controller.ScopeRunsControl},
	{"PUT /api/v1/crons/{id}/override", controller.ScopeRunsControl},
	{"DELETE /api/v1/crons/{id}/override", controller.ScopeRunsControl},
	{"DELETE /api/v1/runs/{id}", controller.ScopeAdmin},
	{"GET /api/v1/team/github-app", controller.ScopeRunsRead},
	{"DELETE /api/v1/team/github-app/installations/{installation_id}", controller.ScopeTeamAdmin},
	{"GET /api/v1/team/github-app/installations/{installation_id}/repositories", controller.ScopeRunsRead},
	{"GET /api/v1/team/github-app/triggers", controller.ScopeRunsRead},
	{"PUT /api/v1/team/github-app/triggers", controller.ScopeTeamAdmin},
	{"DELETE /api/v1/team/github-app/triggers", controller.ScopeTeamAdmin},
}

// safety: a membership role, which the controller resolves on every request for the
// session's active team, decides these routes, so the dashboard adds no scope of its own.
var identityProxyRoutes = []proxyRoute{
	{"GET /api/v1/me", ""},
	{"POST /api/v1/me/active-team", ""},
	{"POST /api/v1/teams", ""},
	{"PATCH /api/v1/team", ""},
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

// safety: the dashboard reads logs on behalf of a browser session, so the logs bearer
// never carries a delete or an append off the browser-facing listener.
var logsProxyRoutes = []proxyRoute{
	{"GET /api/v1/logs/search", controller.ScopeLogsRead},
	{"GET /api/v1/logs/{runID}", controller.ScopeLogsRead},
	{"GET /api/v1/logs/{runID}/{nodeID}", controller.ScopeLogsRead},
	{"GET /api/v1/logs/{runID}/{nodeID}/stream", controller.ScopeLogsRead},
}

func proxyAllowList(proxy http.Handler) http.Handler {
	mux := http.NewServeMux()
	for _, route := range append(slices.Clone(proxyRoutes), identityProxyRoutes...) {
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
		if slices.Contains(principal.Scopes, controller.ScopeAdmin) ||
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
