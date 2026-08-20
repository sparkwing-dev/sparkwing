package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestLateSuccessTimeoutRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"absoluteLateActionPipe.Plan":   false,
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
	foundAbsoluteAssertion := false
	usesAtomicAbsoluteForce := false
	foundSharedAssertion := false
	invokesForce := false
	delegatesForce := func(fn *ast.FuncDecl, selectorName string) bool {
		found := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok || callee.Name != "assertForcedTimeout" {
				return true
			}
			for _, arg := range call.Args {
				sel, ok := arg.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, pkgOK := sel.X.(*ast.Ident)
				if pkgOK && pkg.Name == "orchestrator" && sel.Sel.Name == selectorName {
					found = true
				}
			}
			return true
		})
		return found
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Name.Name == "assertForcedNoProgressTimeout" {
			foundAssertion = true
			usesAtomicForce = delegatesForce(fn, "ForceProgressTimeoutForTest")
		}
		if fn.Name.Name == "assertForcedAbsoluteTimeout" {
			foundAbsoluteAssertion = true
			usesAtomicAbsoluteForce = delegatesForce(fn, "ForceNodeTimeoutForTest")
		}
		if fn.Name.Name == "assertForcedTimeout" {
			foundSharedAssertion = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee, ok := call.Fun.(*ast.Ident)
				if ok && callee.Name == "force" {
					invokesForce = true
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
				t.Errorf("%s uses time.Sleep; synchronize completion through its timeout controller", name)
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
	if !foundAbsoluteAssertion {
		t.Error("required timeout assertion helper assertForcedAbsoluteTimeout not found")
	} else if !usesAtomicAbsoluteForce {
		t.Error("assertForcedAbsoluteTimeout must require an atomic timeout transition")
	}
	if !foundSharedAssertion {
		t.Error("required shared timeout assertion helper assertForcedTimeout not found")
	} else if !invokesForce {
		t.Error("assertForcedTimeout must invoke its force function")
	}
}
