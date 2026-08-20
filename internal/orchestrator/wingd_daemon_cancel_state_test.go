package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDaemonFirstCancelRegressionDoesNotRaceTransientStallVerdict(t *testing.T) {
	const target = "TestWingd_DaemonFirstCancelReleasesHolderAndPromotesWaiter"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wingd_e2e_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse wingd_e2e_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != target || fn.Body == nil {
			continue
		}
		var daemonReady, waiterReady, cancelCall token.Pos
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.KeyValueExpr:
				key, ok := node.Key.(*ast.Ident)
				if ok && (key.Name == "StallInterval" || key.Name == "StallWindow") {
					t.Errorf("%s configures %s at %s", target, key.Name, fset.Position(node.Pos()))
				}
			case *ast.SelectorExpr:
				if node.Sel.Name == "Stalled" || node.Sel.Name == "Recovery" {
					t.Errorf("%s observes transient %s state at %s", target, node.Sel.Name, fset.Position(node.Pos()))
				}
			case *ast.CallExpr:
				switch call := node.Fun.(type) {
				case *ast.Ident:
					switch call.Name {
					case "startWingd":
						daemonReady = call.Pos()
					case "awaitWaiter":
						waiterReady = call.Pos()
					case "findWingdHolder":
						t.Errorf("%s polls a transient holder verdict at %s", target, fset.Position(call.Pos()))
					}
				case *ast.SelectorExpr:
					pkg, ok := call.X.(*ast.Ident)
					if ok && pkg.Name == "wingdclient" && call.Sel.Name == "Cancel" {
						cancelCall = call.Pos()
					}
				}
			}
			return true
		})
		if daemonReady == token.NoPos || waiterReady == token.NoPos || cancelCall == token.NoPos {
			t.Fatalf("%s must start wingd, observe the persisted waiter, and cancel through wingdclient", target)
		}
		if !(daemonReady < waiterReady && waiterReady < cancelCall) {
			t.Fatalf("%s must establish daemon and waiter readiness before cancellation", target)
		}
		return
	}
	t.Fatalf("%s not found", target)
}
