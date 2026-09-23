package web

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/controller"
)

const proxyTestCSRF = "session-token"

func proxyTestDashboard(t *testing.T, scopes []string) (http.Handler, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var reached []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/auth/session" {
			_ = json.NewEncoder(w).Encode(sessionResp{
				Principal: "alice",
				Scopes:    scopes,
				CSRFToken: proxyTestCSRF,
				ExpiresAt: time.Now().Add(time.Hour).Unix(),
			})
			return
		}
		mu.Lock()
		reached = append(reached, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(upstream.Close)
	handler := HandlerFromOptionsWithBundle(HandlerOptions{
		ControllerURL: upstream.URL,
		LogsURL:       upstream.URL,
		Token:         "service-token",
		RequireLogin:  true,
	}, authTestBundle)
	return handler, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string{}, reached...)
	}
}

func proxyTestRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "https://dashboard.example.com"+path, strings.NewReader("{}"))
	req.Header.Set("Origin", "https://dashboard.example.com")
	req.Header.Set(csrfHeaderName, proxyTestCSRF)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "session-1"})
	req.AddCookie(&http.Cookie{Name: csrfCookieName, Value: proxyTestCSRF})
	return req
}

func TestProxyAllowList_SessionCannotReachUnproxiedControllerRoutes(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("slow: 0.4s of real work; the fast class runs under -short")
	}
	handler, reached := proxyTestDashboard(t, []string{controller.ScopeAdmin})
	for _, test := range []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/tokens"},
		{http.MethodGet, "/api/v1/secrets/deploy-key"},
		{http.MethodPost, "/api/v1/users"},
		{http.MethodGet, "/api/v1/users"},
		{http.MethodPost, "/api/v1/runs"},
		{http.MethodGet, "/api/v1/queue/state"},
		{http.MethodDelete, "/api/v1/logs/r1"},
		{http.MethodPost, "/api/v1/logs/r1/n1"},
		{http.MethodGet, "/api/v1/logs/r1/n1/tail"},
		{http.MethodPost, "/api/v1/team/github-app/connect"},
		{http.MethodPost, "/api/v1/team/github-app/connect/complete"},
		{http.MethodPost, "/api/v1/team/github-app/connect/available"},
		{http.MethodPost, "/api/v1/team/github-app/connect/select"},
		{http.MethodDelete, "/api/v1/github-app/installations/42"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, proxyTestRequest(test.method, test.path))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
	if got := reached(); len(got) != 0 {
		t.Fatalf("unproxied routes reached the controller: %v", got)
	}
}

func TestProxyAllowList_SessionScopesGateProxiedRoutes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		scopes []string
		method string
		path   string
		want   int
	}{
		{"reader reads runs", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/runs", http.StatusNoContent},
		{"reader cannot cancel", []string{controller.ScopeRunsRead}, http.MethodPost, "/api/v1/runs/r1/cancel", http.StatusForbidden},
		{"operator cancels", []string{controller.ScopeRunsControl}, http.MethodPost, "/api/v1/runs/r1/cancel", http.StatusNoContent},
		{"operator cannot delete", []string{controller.ScopeRunsControl}, http.MethodDelete, "/api/v1/runs/r1", http.StatusForbidden},
		{"admin deletes", []string{controller.ScopeAdmin}, http.MethodDelete, "/api/v1/runs/r1", http.StatusNoContent},
		{"reader cannot approve", []string{controller.ScopeRunsRead}, http.MethodPost, "/api/v1/runs/r1/approvals/gate", http.StatusForbidden},
		{"approver approves", []string{controller.ScopeApprovalsWrite}, http.MethodPost, "/api/v1/runs/r1/approvals/gate", http.StatusNoContent},
		{"scopeless session reads nothing", nil, http.MethodGet, "/api/v1/runs", http.StatusForbidden},
		{"reader reads crons", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/crons", http.StatusNoContent},
		{"reader reads one cron", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/crons/crn_1", http.StatusNoContent},
		{"reader cannot pause a cron", []string{controller.ScopeRunsRead}, http.MethodPost, "/api/v1/crons/crn_1/pause", http.StatusForbidden},
		{"reader cannot push crons", []string{controller.ScopeRunsRead}, http.MethodPut, "/api/v1/crons/repos", http.StatusForbidden},
		{"operator pushes crons", []string{controller.ScopeRunsControl}, http.MethodPut, "/api/v1/crons/repos", http.StatusNoContent},
		{"operator runs a cron", []string{controller.ScopeRunsControl}, http.MethodPost, "/api/v1/crons/crn_1/run", http.StatusNoContent},
		{"operator clears an override", []string{controller.ScopeRunsControl}, http.MethodDelete, "/api/v1/crons/crn_1/override", http.StatusNoContent},
		{"scopeless session reads no crons", nil, http.MethodGet, "/api/v1/crons", http.StatusForbidden},
		{"reader searches logs", []string{controller.ScopeLogsRead}, http.MethodGet, "/api/v1/logs/search", http.StatusNoContent},
		{"reader reads run logs", []string{controller.ScopeLogsRead}, http.MethodGet, "/api/v1/logs/r1", http.StatusNoContent},
		{"reader streams node logs", []string{controller.ScopeLogsRead}, http.MethodGet, "/api/v1/logs/r1/n1/stream", http.StatusNoContent},
		{"run reader cannot read logs", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/logs/search", http.StatusForbidden},
		{"scopeless session reads no logs", nil, http.MethodGet, "/api/v1/logs/r1/n1", http.StatusForbidden},
		{"reader reads github app", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/team/github-app", http.StatusNoContent},
		{"reader reads installation repositories", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/team/github-app/installations/42/repositories", http.StatusNoContent},
		{"reader reads subscriptions", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/team/github-app/triggers", http.StatusNoContent},
		{"editor cannot subscribe", []string{controller.ScopeRunsRead, controller.ScopeRunsWrite, controller.ScopeRunsControl}, http.MethodPut, "/api/v1/team/github-app/triggers", http.StatusForbidden},
		{"editor cannot unsubscribe", []string{controller.ScopeRunsRead, controller.ScopeRunsControl}, http.MethodDelete, "/api/v1/team/github-app/triggers", http.StatusForbidden},
		{"editor cannot disconnect", []string{controller.ScopeRunsRead, controller.ScopeRunsControl}, http.MethodDelete, "/api/v1/team/github-app/installations/42", http.StatusForbidden},
		{"owner subscribes", []string{controller.ScopeRunsRead, controller.ScopeTeamAdmin}, http.MethodPut, "/api/v1/team/github-app/triggers", http.StatusNoContent},
		{"owner disconnects", []string{controller.ScopeRunsRead, controller.ScopeTeamAdmin}, http.MethodDelete, "/api/v1/team/github-app/installations/42", http.StatusNoContent},
		{"scopeless session reads no github app", nil, http.MethodGet, "/api/v1/team/github-app", http.StatusForbidden},
		{"reader lists secrets", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/secrets", http.StatusNoContent},
		{"scopeless session lists no secrets", nil, http.MethodGet, "/api/v1/secrets", http.StatusForbidden},
		{"editor cannot write a secret", []string{controller.ScopeRunsRead, controller.ScopeRunsControl}, http.MethodPost, "/api/v1/secrets", http.StatusForbidden},
		{"editor cannot delete a secret", []string{controller.ScopeRunsRead, controller.ScopeRunsControl}, http.MethodDelete, "/api/v1/secrets/API_KEY", http.StatusForbidden},
		{"owner writes a secret", []string{controller.ScopeTeamAdmin}, http.MethodPost, "/api/v1/secrets", http.StatusNoContent},
		{"owner deletes a secret", []string{controller.ScopeTeamAdmin}, http.MethodDelete, "/api/v1/secrets/API_KEY", http.StatusNoContent},
		{"operator writes a secret", []string{controller.ScopeAdmin}, http.MethodPost, "/api/v1/secrets", http.StatusNoContent},
		{"reader reads billing", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/team/billing", http.StatusNoContent},
		{"editor cannot check out", []string{controller.ScopeRunsRead, controller.ScopeRunsControl}, http.MethodPost, "/api/v1/team/billing/checkout", http.StatusForbidden},
		{"owner checks out", []string{controller.ScopeTeamAdmin}, http.MethodPost, "/api/v1/team/billing/checkout", http.StatusNoContent},
		{"reader lists git credentials", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/team/git-credentials", http.StatusNoContent},
		{"editor cannot store a git credential", []string{controller.ScopeRunsRead, controller.ScopeRunsControl}, http.MethodPost, "/api/v1/team/git-credentials", http.StatusForbidden},
		{"owner stores a git credential", []string{controller.ScopeTeamAdmin}, http.MethodPost, "/api/v1/team/git-credentials", http.StatusNoContent},
		{"owner confirms a git credential", []string{controller.ScopeTeamAdmin}, http.MethodPost, "/api/v1/team/git-credentials/github.com/confirm", http.StatusNoContent},
		{"owner deletes a git credential", []string{controller.ScopeTeamAdmin}, http.MethodDelete, "/api/v1/team/git-credentials/github.com", http.StatusNoContent},
		{"reader cannot list credential releases", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/team/git-credentials/releases", http.StatusForbidden},
		{"owner lists credential releases", []string{controller.ScopeTeamAdmin}, http.MethodGet, "/api/v1/team/git-credentials/releases", http.StatusNoContent},
		{"editor cannot opt a machine in", []string{controller.ScopeRunsRead, controller.ScopeRunsWrite}, http.MethodPut, "/api/v1/team/runner-tokens/swr_ab12/git-credentials", http.StatusForbidden},
		{"owner opts a machine in", []string{controller.ScopeTeamAdmin}, http.MethodPut, "/api/v1/team/runner-tokens/swr_ab12/git-credentials", http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler, reached := proxyTestDashboard(t, test.scopes)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, proxyTestRequest(test.method, test.path))
			if rec.Code != test.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, test.want, rec.Body.String())
			}
			forwarded := len(reached()) == 1
			if forwarded != (test.want == http.StatusNoContent) {
				t.Fatalf("forwarded to controller = %t at status %d", forwarded, rec.Code)
			}
		})
	}
}

func TestProxy_SecretsResponsesAreNoStore(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/secrets"},
		{http.MethodPost, "/api/v1/secrets"},
		{http.MethodDelete, "/api/v1/secrets/API_KEY"},
	} {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			t.Parallel()
			handler, _ := proxyTestDashboard(t, []string{controller.ScopeTeamAdmin, controller.ScopeRunsRead})
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, proxyTestRequest(test.method, test.path))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
			if got := rec.Header().Get("Pragma"); got != "no-cache" {
				t.Errorf("Pragma = %q, want no-cache", got)
			}
		})
	}
}

func TestProxyRoutes_ScopesMatchControllerRegistrations(t *testing.T) {
	t.Parallel()
	registered := controllerRouteScopes(t)
	for _, route := range proxyRoutes {
		scopes, ok := registered[route.pattern]
		if !ok {
			t.Errorf("proxy allows %q, which the controller does not register", route.pattern)
			continue
		}
		if !slices.Contains(scopes, route.scope) {
			t.Errorf("proxy guards %q with %s; the controller registers it at %v",
				route.pattern, route.scope, scopes)
		}
	}
}

func TestLogsProxyRoutes_ScopesMatchLogsRegistrations(t *testing.T) {
	t.Parallel()
	registered := logsRouteScopes(t)
	for _, route := range logsProxyRoutes {
		if !strings.HasPrefix(route.pattern, http.MethodGet+" ") {
			t.Errorf("logs proxy allows %q; the dashboard forwards reads only", route.pattern)
			continue
		}
		scopes, ok := registered[route.pattern]
		if !ok {
			t.Errorf("logs proxy allows %q, which the logs service does not register", route.pattern)
			continue
		}
		if !slices.Contains(scopes, route.scope) {
			t.Errorf("logs proxy guards %q with %s; the logs service registers it at %v",
				route.pattern, route.scope, scopes)
		}
	}
}

func controllerRouteScopes(t *testing.T) map[string][]string {
	t.Helper()
	return routeScopes(t, "../../pkg/controller/server.go", map[string]string{
		"ScopeRunsRead":       controller.ScopeRunsRead,
		"ScopeRunsWrite":      controller.ScopeRunsWrite,
		"ScopeRunsControl":    controller.ScopeRunsControl,
		"ScopeNodesClaim":     controller.ScopeNodesClaim,
		"ScopeLogsRead":       controller.ScopeLogsRead,
		"ScopeLogsWrite":      controller.ScopeLogsWrite,
		"ScopeTriggersRead":   controller.ScopeTriggersRead,
		"ScopeTriggersClaim":  controller.ScopeTriggersClaim,
		"ScopeRunsState":      controller.ScopeRunsState,
		"ScopeSecretsRead":    controller.ScopeSecretsRead,
		"ScopeApprovalsWrite": controller.ScopeApprovalsWrite,
		"ScopeTeamAdmin":      controller.ScopeTeamAdmin,
		"ScopeAdmin":          controller.ScopeAdmin,
		"ScopeCreditsGrant":   controller.ScopeCreditsGrant,
	})
}

func logsRouteScopes(t *testing.T) map[string][]string {
	t.Helper()
	return routeScopes(t, "../../pkg/logs/server.go", map[string]string{
		"scopeLogsRead":   controller.ScopeLogsRead,
		"scopeLogsWrite":  controller.ScopeLogsWrite,
		"scopeAdmin":      controller.ScopeAdmin,
		"scopeLogsDelete": controller.ScopeLogsDelete,
	})
}

func routeScopes(t *testing.T, source string, values map[string]string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, source, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) < 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Handle" {
			return true
		}
		if recv, ok := sel.X.(*ast.Ident); !ok || recv.Name != "mux" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		wrapped, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			return true
		}
		if wrapperName(wrapped.Fun) != "requireScope" || len(wrapped.Args) == 0 {
			return true
		}
		for i, arg := range wrapped.Args {
			if i == 1 {
				continue
			}
			ident, ok := arg.(*ast.Ident)
			if !ok {
				return true
			}
			value, ok := values[ident.Name]
			if !ok {
				t.Errorf("%s registers a route at unknown scope constant %s", source, ident.Name)
				return true
			}
			out[pattern] = append(out[pattern], value)
		}
		return true
	})
	if len(out) == 0 {
		t.Fatalf("parsed no routes from %s; the guard would pass vacuously", source)
	}
	return out
}

func wrapperName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	}
	return ""
}
