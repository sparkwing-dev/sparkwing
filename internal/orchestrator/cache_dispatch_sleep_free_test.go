package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCacheDispatchStatePollingDoesNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"waitForConcurrencyHolder":   false,
		"waitForSpawnedChildTrigger": false,
		"TestConcurrency_RunAndAwaitUnboundedClaimedChildAdmissionProtectsParentDispatch": false,
		"TestConcurrency_RunAndAwaitNoProgressTimeoutResumesAfterAdmissionWait":           false,
		"TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused":        false,
		"TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget":              false,
		"TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline":                    false,
		"TestConcurrency_RunAndAwaitParentTimeoutCountsMissedPromotionAsAdmissionWait":    false,
		"TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait":         false,
		"TestConcurrency_RunAndAwaitParentTimeoutCountsSlowChildPlanning":                 false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cache_dispatch_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cache_dispatch_test.go: %v", err)
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
				t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_dispatch_test.go does not declare %s", name)
		}
	}
}
