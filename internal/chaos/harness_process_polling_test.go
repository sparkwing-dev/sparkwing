package chaos

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestProcessPollingRegressionsDoNotSleep(t *testing.T) {
	targets := map[string]bool{
		"TestWatchActorReapsExitedProcessAndRecordsFinalOutput":           false,
		"TestManagedDaemonBoundsRepeatedIgnoreTermDescendantChurn":        false,
		"TestProcessGuardAcceptsSoakScaleDescendantZombieBurst":           false,
		"TestProcessGuardExemptsZombiesRetainedByAReportedCleanupFailure": false,
		"awaitGuardViolation": false,
		"pollProcessState":    false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "harness_process_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
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
				t.Errorf("%s: poll process state with an owned deadline", fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("target declaration %s not found", name)
		}
	}
}
