//go:build unix

package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCapacityReconciliationRegressionDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "capacity_reconcile_unix_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse capacity_reconcile_unix_test.go: %v", err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestRecordRunProfile_SDKBurnerPeakNotDoubled" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
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
				t.Error("capacity reconciliation regression uses time.Sleep; synchronize on persisted samples")
			}
			return true
		})
	}
	if !found {
		t.Error("required capacity reconciliation regression not found")
	}
}
