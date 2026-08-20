package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDispatchWatchdogStateTransitionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"TestDispatchWatchdog_ArmsAfterAdmissionWaitEnds":       false,
		"TestDispatchWatchdog_WakesWhenWedgedSiblingStarts":     false,
		"TestDispatchWatchdog_PausesWhenRunningSiblingFinishes": false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "dispatch_watchdog_admission_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse dispatch_watchdog_admission_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
		usesObservation := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForWatchdogObservation" {
				usesObservation = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Sleep" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
					t.Errorf("%s uses time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
				}
			}
			return true
		})
		if !usesObservation {
			t.Errorf("%s does not use waitForWatchdogObservation", fn.Name.Name)
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s declaration not found", name)
		}
	}
}
