package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

// Every route registered on the authenticated mux must pass through
// requireScope; an endpoint registered bare would be reachable by any
// authenticated principal regardless of token scope. Routes that are
// deliberately public (login, bootstrap, health, metrics) live on the
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
