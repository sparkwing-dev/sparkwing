package web

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
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
		{"writer cancels", []string{controller.ScopeRunsWrite}, http.MethodPost, "/api/v1/runs/r1/cancel", http.StatusNoContent},
		{"writer cannot delete", []string{controller.ScopeRunsWrite}, http.MethodDelete, "/api/v1/runs/r1", http.StatusForbidden},
		{"admin deletes", []string{controller.ScopeAdmin}, http.MethodDelete, "/api/v1/runs/r1", http.StatusNoContent},
		{"reader cannot approve", []string{controller.ScopeRunsRead}, http.MethodPost, "/api/v1/runs/r1/approvals/gate", http.StatusForbidden},
		{"approver approves", []string{controller.ScopeApprovalsWrite}, http.MethodPost, "/api/v1/runs/r1/approvals/gate", http.StatusNoContent},
		{"scopeless session reads nothing", nil, http.MethodGet, "/api/v1/runs", http.StatusForbidden},
		{"reader searches logs", []string{controller.ScopeLogsRead}, http.MethodGet, "/api/v1/logs/search", http.StatusNoContent},
		{"reader reads run logs", []string{controller.ScopeLogsRead}, http.MethodGet, "/api/v1/logs/r1", http.StatusNoContent},
		{"reader streams node logs", []string{controller.ScopeLogsRead}, http.MethodGet, "/api/v1/logs/r1/n1/stream", http.StatusNoContent},
		{"run reader cannot read logs", []string{controller.ScopeRunsRead}, http.MethodGet, "/api/v1/logs/search", http.StatusForbidden},
		{"scopeless session reads no logs", nil, http.MethodGet, "/api/v1/logs/r1/n1", http.StatusForbidden},
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

func TestProxyRoutes_ScopesMatchControllerRegistrations(t *testing.T) {
	t.Parallel()
	registered := controllerRouteScopes(t)
	for _, route := range proxyRoutes {
		scope, ok := registered[route.pattern]
		if !ok {
			t.Errorf("proxy allows %q, which the controller does not register", route.pattern)
			continue
		}
		if scope != route.scope {
			t.Errorf("proxy guards %q with %s; the controller registers it at %s",
				route.pattern, route.scope, scope)
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
		scope, ok := registered[route.pattern]
		if !ok {
			t.Errorf("logs proxy allows %q, which the logs service does not register", route.pattern)
			continue
		}
		if scope != route.scope {
			t.Errorf("logs proxy guards %q with %s; the logs service registers it at %s",
				route.pattern, route.scope, scope)
		}
	}
}

func controllerRouteScopes(t *testing.T) map[string]string {
	t.Helper()
	return routeScopes(t, "../../pkg/controller/server.go", map[string]string{
		"ScopeRunsRead":       controller.ScopeRunsRead,
		"ScopeRunsWrite":      controller.ScopeRunsWrite,
		"ScopeNodesClaim":     controller.ScopeNodesClaim,
		"ScopeLogsRead":       controller.ScopeLogsRead,
		"ScopeLogsWrite":      controller.ScopeLogsWrite,
		"ScopeTriggersRead":   controller.ScopeTriggersRead,
		"ScopeTriggersClaim":  controller.ScopeTriggersClaim,
		"ScopeRunsState":      controller.ScopeRunsState,
		"ScopeSecretsRead":    controller.ScopeSecretsRead,
		"ScopeApprovalsWrite": controller.ScopeApprovalsWrite,
		"ScopeAdmin":          controller.ScopeAdmin,
	})
}

func logsRouteScopes(t *testing.T) map[string]string {
	t.Helper()
	return routeScopes(t, "../../pkg/logs/server.go", map[string]string{
		"scopeLogsRead":  controller.ScopeLogsRead,
		"scopeLogsWrite": controller.ScopeLogsWrite,
		"scopeAdmin":     controller.ScopeAdmin,
	})
}

func routeScopes(t *testing.T, source string, values map[string]string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, source, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
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
		ident, ok := wrapped.Args[0].(*ast.Ident)
		if !ok {
			return true
		}
		value, ok := values[ident.Name]
		if !ok {
			t.Errorf("%s registers a route at unknown scope constant %s", source, ident.Name)
			return true
		}
		out[pattern] = value
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
