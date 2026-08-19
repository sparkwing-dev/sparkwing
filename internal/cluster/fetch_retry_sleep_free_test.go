package cluster

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestFetchRetryRegressionsDoNotSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fetch_retry_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
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
			t.Errorf("%s: synchronize cancellation with the fetch attempt", fset.Position(call.Pos()))
		}
		return true
	})
}
