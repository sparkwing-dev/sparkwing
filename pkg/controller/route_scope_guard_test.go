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
