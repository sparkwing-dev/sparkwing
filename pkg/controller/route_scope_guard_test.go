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
		"GET /api/v1/services":              true,
		"POST /api/v1/auth/login":           true,
		"POST /api/v1/auth/logout":          true,
		"GET /api/v1/auth/session":          true,
		"GET /api/v1/auth/bootstrap-needed": true,
		"GET /metrics":                      true,
		"POST /webhooks/github/{pipeline}":  true,
		"/":                                 true,
	}
	got := routesRegisteredOn(t, "server.go", "router")
	if !maps.Equal(got, want) {
		t.Errorf("outer router routes = %v; want reviewed set %v", got, want)
	}
}

// Every route registered on the authenticated mux must pass through
// requireScope; an endpoint registered bare would be reachable by any
// authenticated principal regardless of token scope. Routes that are
// deliberately public (login, bootstrap probe, health, metrics) live on the
// outer router, which this guard does not constrain. Mux routes that
// deliberately accept any authenticated principal must be listed here
// so the exception is a conscious, reviewed act.
func TestRouteGuard_EveryMuxRouteRequiresScope(t *testing.T) {
	anyAuthenticated := map[string]bool{
		"GET /api/v1/auth/whoami": true,
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
		if wrapped, ok := call.Args[1].(*ast.CallExpr); ok {
			if fn, ok := wrapped.Fun.(*ast.Ident); ok && fn.Name == "requireScope" {
				return true
			}
		}
		t.Errorf("server.go:%d: mux route registered without requireScope",
			fset.Position(call.Pos()).Line)
		return true
	})
}

// The loopback controller claims a node process cannot tell it from
// the real one. Prose cannot hold that: a route renamed or re-scoped in
// server.go leaves the loopback answering a path nothing calls, or
// answering a call at a scope the real controller refuses.
//
// Both route tables are read out of the source, so the assertion is on
// what is registered rather than on what a comment says: every pattern
// the loopback serves must be registered by server.go, at the same
// scope. The reverse does not hold and must not -- the loopback serves
// a subset, which is the whole point of it.
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

// muxRoutes reads the `mux.Handle("<pattern>", requireScope(Scope..., ...))`
// registrations out of one file, as pattern -> scope constant name.
// Routes on the outer public router are not mux routes and are skipped,
// which is what the requireScope guard above already assumes.
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
		wrapped, ok := call.Args[1].(*ast.CallExpr)
		if !ok {
			return true
		}
		fn, ok := wrapped.Fun.(*ast.Ident)
		if !ok || fn.Name != "requireScope" || len(wrapped.Args) == 0 {
			return true
		}
		scope, ok := wrapped.Args[0].(*ast.Ident)
		if !ok {
			return true
		}
		out[pattern] = scope.Name
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

// The SDK's approval timeout policy strings and the store's resolution
// constants are independent declarations of one wire vocabulary; the
// orchestrator serializes the former and compares against the latter.
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
