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

func controllerRouteScopes(t *testing.T) map[string]string {
	t.Helper()
	values := map[string]string{
		"ScopeRunsRead":       controller.ScopeRunsRead,
		"ScopeRunsWrite":      controller.ScopeRunsWrite,
		"ScopeNodesClaim":     controller.ScopeNodesClaim,
		"ScopeLogsRead":       controller.ScopeLogsRead,
		"ScopeLogsWrite":      controller.ScopeLogsWrite,
		"ScopeTriggersRead":   controller.ScopeTriggersRead,
		"ScopeApprovalsWrite": controller.ScopeApprovalsWrite,
		"ScopeAdmin":          controller.ScopeAdmin,
	}
	const source = "../../pkg/controller/server.go"
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
		if fn, ok := wrapped.Fun.(*ast.Ident); !ok || fn.Name != "requireScope" || len(wrapped.Args) == 0 {
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
