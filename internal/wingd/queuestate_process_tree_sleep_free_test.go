//go:build darwin || linux

package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestQueueStateProcessTreeRegressionDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "queuestate_process_tree_unix_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse queuestate_process_tree_unix_test.go: %v", err)
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
			t.Errorf("queuestate_process_tree_unix_test.go contains time.Sleep at %s", fset.Position(call.Pos()))
		}
		return true
	})
}
