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
			}
			return true
		})
		return
	}
	t.Fatalf("%s not found", target)
}
