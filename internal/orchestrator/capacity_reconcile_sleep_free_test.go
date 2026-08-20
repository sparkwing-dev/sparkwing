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
	targets := map[string]bool{
		"TestRecordRunProfile_SDKBurnerPeakNotDoubled": false,
		"reconcileSink.Push":                           false,
		"waitForReconcileSampleAfter":                  false,
	}
	waitCalls := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			if receiver, ok := fn.Recv.List[0].Type.(*ast.Ident); ok {
				name = receiver.Name + "." + name
			}
		}
		if _, ok := targets[name]; !ok {
			continue
		}
		targets[name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if callee, ok := call.Fun.(*ast.Ident); ok && name == "TestRecordRunProfile_SDKBurnerPeakNotDoubled" && callee.Name == "waitForReconcileSampleAfter" {
				waitCalls++
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" {
				t.Errorf("%s uses time.Sleep; synchronize on persisted samples", name)
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("required capacity reconciliation target %s not found", name)
		}
	}
	if waitCalls != 2 {
		t.Errorf("capacity reconciliation regression waits for %d persisted sample boundaries, want 2", waitCalls)
	}
}
