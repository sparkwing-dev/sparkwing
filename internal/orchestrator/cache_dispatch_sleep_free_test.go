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
		"waitForNodeTimeoutPaused":                                                     false,
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
	cancellationChecksPausedController := false
	cancellationRejectsPausedExpiry := false
	remainingBudgetChecksPausedController := false
	remainingBudgetRejectsPausedExpiry := false
	remainingBudgetInspectsControllerState := false
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
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused" && ok && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "ProgressTimeoutPausedForTest":
					cancellationChecksPausedController = true
				case "ExpireProgressTimeoutForTest":
					cancellationRejectsPausedExpiry = true
				}
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget" && ok && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "NodeTimeoutPausedForTest":
					remainingBudgetChecksPausedController = true
				case "ForceNodeTimeoutForTest":
					remainingBudgetRejectsPausedExpiry = true
				case "NodeTimeoutStateForTest":
					remainingBudgetInspectsControllerState = true
				}
			}
			isPollingHelper := fn.Name.Name == "waitForConcurrencyHolder" || fn.Name.Name == "waitForNodeTimeoutPaused" || fn.Name.Name == "waitForPlanAdmissionWaiter" || fn.Name.Name == "waitForSpawnedChildTrigger"
			isCancellationRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused"
			isRemainingBudgetRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget"
			if (isPollingHelper || isCancellationRegression || isRemainingBudgetRegression) && sel.Sel.Name == "Sleep" && ok && pkg.Name == "time" {
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
		isHelper := fn.Name.Name == "waitForConcurrencyHolder" || fn.Name.Name == "waitForNodeTimeoutPaused" || fn.Name.Name == "waitForPlanAdmissionWaiter" || fn.Name.Name == "waitForSpawnedChildTrigger"
		if !isHelper && !usesSpawnWait {
			t.Errorf("%s does not use waitForSpawnedChildTrigger", fn.Name.Name)
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_dispatch_test.go does not declare %s", name)
		}
	}
	if !cancellationChecksPausedController {
		t.Error("parent-cancellation regression does not inspect the paused timeout controller")
	}
	if !cancellationRejectsPausedExpiry {
		t.Error("parent-cancellation regression does not reject timeout expiry while admission is paused")
	}
	if !remainingBudgetChecksPausedController {
		t.Error("remaining-budget regression does not inspect the paused timeout controller")
	}
	if !remainingBudgetRejectsPausedExpiry {
		t.Error("remaining-budget regression does not reject forced timeout while admission is paused")
	}
	if !remainingBudgetInspectsControllerState {
		t.Error("remaining-budget regression does not compare paused and resumed timeout state")
	}
}
