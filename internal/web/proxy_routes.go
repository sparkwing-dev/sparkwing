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
	{"POST /api/v1/runs/{id}/cancel", controller.ScopeRunsWrite},
	{"POST /api/v1/runs/{id}/retry", controller.ScopeRunsWrite},
	{"POST /api/v1/runs/{id}/nodes/{nodeID}/release", controller.ScopeRunsWrite},
	{"POST /api/v1/runs/{id}/approvals/{nodeID}", controller.ScopeApprovalsWrite},
	{"DELETE /api/v1/runs/{id}", controller.ScopeAdmin},
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
	for _, route := range proxyRoutes {
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
		if !ok {
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
