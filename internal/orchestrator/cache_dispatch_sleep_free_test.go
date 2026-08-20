package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCacheDispatchStatePollingDoesNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"waitForConcurrencyHolder":                                                     false,
		"waitForPlanAdmissionWaiter":                                                   false,
		"waitForSpawnedChildTrigger":                                                   false,
		"testRunAndAwaitAdmissionOutlivesDispatchWatchdog":                             false,
		"TestConcurrency_RunAndAwaitNoProgressTimeoutResumesAfterAdmissionWait":        false,
		"TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused":     false,
		"TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget":           false,
		"TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline":                 false,
		"TestConcurrency_RunAndAwaitParentTimeoutCountsMissedPromotionAsAdmissionWait": false,
		"TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait":      false,
		"TestConcurrency_RunAndAwaitParentTimeoutCountsSlowChildPlanning":              false,
	}
	planWaiterCallers := map[string]bool{
		"testRunAndAwaitAdmissionOutlivesDispatchWatchdog":                             true,
		"TestConcurrency_RunAndAwaitNoProgressTimeoutResumesAfterAdmissionWait":        true,
		"TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused":     true,
		"TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget":           true,
		"TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline":                 true,
		"TestConcurrency_RunAndAwaitParentTimeoutCountsMissedPromotionAsAdmissionWait": true,
		"TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait":      true,
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
		usesPlanWait := false
		usesSpawnWait := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForSpawnedChildTrigger" {
				usesSpawnWait = true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForPlanAdmissionWaiter" {
				usesPlanWait = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if fn.Name.Name != "waitForSpawnedChildTrigger" && sel.Sel.Name == "FindSpawnedChildTriggerID" {
				t.Errorf("%s polls FindSpawnedChildTriggerID directly at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			pkg, ok := sel.X.(*ast.Ident)
			isPollingHelper := fn.Name.Name == "waitForConcurrencyHolder" || fn.Name.Name == "waitForPlanAdmissionWaiter" || fn.Name.Name == "waitForSpawnedChildTrigger"
			if isPollingHelper && sel.Sel.Name == "Sleep" && ok && pkg.Name == "time" {
				t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			if planWaiterCallers[fn.Name.Name] && sel.Sel.Name == "GetConcurrencyState" {
				t.Errorf("%s polls GetConcurrencyState directly at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
		if planWaiterCallers[fn.Name.Name] && !usesPlanWait {
			t.Errorf("%s does not use waitForPlanAdmissionWaiter", fn.Name.Name)
		}
		isHelper := fn.Name.Name == "waitForConcurrencyHolder" || fn.Name.Name == "waitForPlanAdmissionWaiter" || fn.Name.Name == "waitForSpawnedChildTrigger"
		if !isHelper && !usesSpawnWait {
			t.Errorf("%s does not use waitForSpawnedChildTrigger", fn.Name.Name)
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_dispatch_test.go does not declare %s", name)
		}
	}
}
