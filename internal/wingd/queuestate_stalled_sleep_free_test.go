package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestQueueStateStalledRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"TestQueueState_RecoveryCommandUsesOwnerRunID":        false,
		"TestQueueState_FlagsStalledHolderWithRecoveryCommand": false,
		"TestQueueState_IdleDescendantTreeStillStalls":         false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "queuestate_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse queuestate_test.go: %v", err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
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
				t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("target function %s not found", name)
		}
	}
}
