package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestNoProgressLateSuccessRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"noProgressLateActionPipe.Plan": false,
		"noProgressLateVerifyPipe.Plan": false,
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modifiers_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse modifiers_test.go: %v", err)
	}
	foundAssertion := false
	usesAtomicForce := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "assertForcedNoProgressTimeout" {
			foundAssertion = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "ForceProgressTimeoutForTest" {
					usesAtomicForce = true
				}
				return true
			})
		}
		if fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		receiver, ok := fn.Recv.List[0].Type.(*ast.Ident)
		if !ok {
			continue
		}
		name := receiver.Name + "." + fn.Name.Name
		if _, ok := targets[name]; !ok {
			continue
		}
		targets[name] = true
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
				t.Errorf("%s uses time.Sleep; synchronize timeout completion through the progress controller", name)
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("required timeout regression owner %s not found", name)
		}
	}
	if !foundAssertion {
		t.Error("required timeout assertion helper assertForcedNoProgressTimeout not found")
	} else if !usesAtomicForce {
		t.Error("assertForcedNoProgressTimeout must require an atomic timeout transition")
	}
}
