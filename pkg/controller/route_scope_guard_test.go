package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestRouteGuard_OuterRouterContainsOnlyReviewedRoutes(t *testing.T) {
	want := map[string]bool{
		"GET /api/v1/health":                true,
		"POST /api/v1/auth/login":           true,
		"POST /api/v1/auth/logout":          true,
		"GET /api/v1/auth/session":          true,
		"GET /api/v1/auth/bootstrap-needed": true,
		"GET /metrics":                      true,
		"POST /webhooks/github/{pipeline}":  true,
		"/":                                 true,
		// safety: a signed-out browser draws the sign-in page from it; it reports only teams and providers.
		"GET /api/v1/capabilities": true,
		// safety: sign-in has no session yet; both answer 404 without a license and a Google client.
		"POST /api/v1/auth/oauth/google/start":    true,
		"POST /api/v1/auth/oauth/google/exchange": true,
		"POST /api/v1/auth/oauth/github/start":    true,
		"POST /api/v1/auth/oauth/github/exchange": true,
		// safety: a GitHub Actions job proves itself with its signed ID token; it answers 404 without an external URL.
		"POST /api/v1/runners/github/exchange": true,
		// safety: a GitHub App delivery proves itself with its signature; it answers 404 without an App.
		"POST /webhooks/github-app": true,
		// safety: cloud providers fetch OIDC metadata and keys unauthenticated; both answer 404 without a key.
		"GET /.well-known/openid-configuration": true,
		"GET /.well-known/jwks.json":            true,
		// safety: the cache proves itself with its operator token and the logs service
		// with a forwarded credential, which the handlers check themselves.
		"POST /internal/storage/reserve":  true,
		"POST /internal/storage/commit":   true,
		"POST /internal/storage/release":  true,
		"POST /internal/downloads/charge": true,
		"POST /api/v1/data/download":      true,
		"POST /internal/egress/totals":    true,
		// safety: upload and commit authenticate a bound grant or runner token and
		// check its live claim inside the handler; capability reports only availability.
		"POST /api/v1/data/upload":      true,
		"POST /api/v1/data/commit":      true,
		"GET /api/v1/data/capabilities": true,
	}
	got := routesRegisteredOn(t, "server.go", "router")
	if !maps.Equal(got, want) {
		t.Errorf("outer router routes = %v; want reviewed set %v", got, want)
	}
}

func TestRouteGuard_EveryMuxRouteRequiresScope(t *testing.T) {
	anyAuthenticated := map[string]bool{
		"GET /api/v1/auth/whoami": true,
		"GET /api/v1/services":    true,
		// safety: these act on the caller's own memberships, so accountPrincipal inside each handler
		// is the gate, and it refuses every caller that is not a signed-in account.
		"GET /api/v1/me":                                      true,
		"DELETE /api/v1/me":                                   true,
		"GET /api/v1/me/team-deletions":                       true,
		"POST /api/v1/me/active-team":                         true,
		"GET /api/v1/me/identities":                           true,
		"POST /api/v1/me/identities/{provider}/link":          true,
		"POST /api/v1/me/identities/{provider}/link/complete": true,
		"DELETE /api/v1/me/identities/{provider}":             true,
		"POST /api/v1/teams":                                  true,
		"POST /api/v1/invitations/{id}/accept":                true,
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != "mux" || len(call.Args) < 2 {
			return true
		}
		if pattern, ok := call.Args[0].(*ast.BasicLit); ok {
			if p, err := strconv.Unquote(pattern.Value); err == nil && anyAuthenticated[p] {
				return true
			}
		}
		if _, ok := routeScope(call.Args[1]); ok {
			return true
		}
		t.Errorf("server.go:%d: mux route registered without a scope gate",
			fset.Position(call.Pos()).Line)
		return true
	})
}

func TestRouteGuard_LoopbackRoutesAreASubsetOfTheController(t *testing.T) {
	server := muxRoutes(t, "server.go")
	loopback := muxRoutes(t, "loopback.go")
	if len(loopback) == 0 {
		t.Fatal("no loopback routes parsed; the guard would pass vacuously")
	}
	for pattern, scope := range loopback {
		want, ok := server[pattern]
		if !ok {
			t.Errorf("loopback serves %q, which server.go does not register", pattern)
			continue
		}
		if want != scope {
			t.Errorf("loopback serves %q at scope %s; server.go registers it at %s",
				pattern, scope, want)
		}
	}
}

func routeScope(handler ast.Expr) (string, bool) {
	call, ok := handler.(*ast.CallExpr)
	if !ok || len(call.Args) == 0 {
		return "", false
	}
	var name string
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	default:
		return "", false
	}
	if name != "requireScope" {
		return "", false
	}
	scope, ok := call.Args[0].(*ast.Ident)
	if !ok {
		return "", false
	}
	return scope.Name, true
}

func muxRoutes(t *testing.T, file string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
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
		scope, ok := routeScope(call.Args[1])
		if !ok {
			return true
		}
		out[pattern] = scope
		return true
	})
	return out
}

func routesRegisteredOn(t *testing.T, file, receiver string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc" {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != receiver {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			t.Errorf("%s:%d: %s route pattern is not a string literal; the guard cannot verify it",
				file, fset.Position(call.Pos()).Line, receiver)
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Errorf("%s:%d: invalid route pattern: %v", file, fset.Position(lit.Pos()).Line, err)
			return true
		}
		if out[pattern] {
			t.Errorf("%s:%d: duplicate %s route %q", file, fset.Position(call.Pos()).Line, receiver, pattern)
		}
		out[pattern] = true
		return true
	})
	return out
}

func TestApprovalTimeoutPolicy_SDKMatchesStoreVocabulary(t *testing.T) {
	pairs := map[string]string{
		string(sparkwing.ApprovalFail):    store.ApprovalOnTimeoutFail,
		string(sparkwing.ApprovalDeny):    store.ApprovalOnTimeoutDeny,
		string(sparkwing.ApprovalApprove): store.ApprovalOnTimeoutApprove,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("sparkwing approval policy %q drifted from store vocabulary %q", got, want)
		}
	}
}
