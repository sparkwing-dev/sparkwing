//go:build !windows

package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestStoreWedgeContentionRegressionsDoNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "store_wedge_contention_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse store_wedge_contention_test.go: %v", err)
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
			t.Errorf("store_wedge_contention_test.go contains time.Sleep at %s", fset.Position(call.Pos()))
		}
		return true
	})
}
