package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestBudgetStartupRegressionsDoNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "budget_startup_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse budget_startup_test.go: %v", err)
	}

	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sleep" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" {
			t.Errorf("budget_startup_test.go contains time.Sleep at %s", fset.Position(call.Pos()))
		}
		return true
	})
}
