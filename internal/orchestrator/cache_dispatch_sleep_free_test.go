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
		"waitForNodeTimeoutResumed":                                                    false,
		"waitForProgressTimeoutResumed":                                                false,
		"observeAdmissionWaitBeyondDispatchTimeout":                                    false,
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
	remainingBudgetControlsRemainder := false
	earlyResumeChecksPausedController := false
	earlyResumeRejectsPausedExpiry := false
	earlyResumeControlsRemainder := false
	noProgressChecksPausedController := false
	noProgressRejectsPausedExpiry := false
	dispatchWatchdogObservesAdmissionWait := false
	dispatchWatchdogUsesObservation := false
	dispatchWatchdogSpansTimeout := false
	multiKeyChecksPausedController := false
	multiKeyRejectsPausedExpiry := false
	multiKeyInspectsControllerState := false
	multiKeyControlsRemainder := false
	multiKeyGateGated := false
	earlyResumeGateGated := false
	cancellationGateUngated := false
	remainingBudgetGateGated := false
	var remainingBudgetSetPos token.Pos
	var remainingBudgetReleasePos token.Pos
	var remainingBudgetSpawnWaitPos token.Pos
	var earlyResumeSetPos token.Pos
	var earlyResumeReleasePos token.Pos
	var earlyResumeSpawnWaitPos token.Pos
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
			if lit, ok := node.(*ast.CompositeLit); ok {
				gateType, isGate := lit.Type.(*ast.Ident)
				if isGate && gateType.Name == "queuedAwaitParentGate" {
					hasProceed := false
					for _, elt := range lit.Elts {
						field, keyed := elt.(*ast.KeyValueExpr)
						if !keyed {
							continue
						}
						name, named := field.Key.(*ast.Ident)
						if named && name.Name == "proceed" {
							hasProceed = true
						}
					}
					switch fn.Name.Name {
					case "TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused":
						cancellationGateUngated = !hasProceed
					case "TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget":
						remainingBudgetGateGated = hasProceed
					case "TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline":
						earlyResumeGateGated = hasProceed
					case "TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait":
						multiKeyGateGated = hasProceed
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForSpawnedChildTrigger" {
				usesSpawnWait = true
				if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget" {
					remainingBudgetSpawnWaitPos = call.Pos()
				} else if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline" {
					earlyResumeSpawnWaitPos = call.Pos()
				}
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForPlanAdmissionWaiter" {
				usesPlanWait = true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "observeAdmissionWaitBeyondDispatchTimeout" && fn.Name.Name == "testRunAndAwaitAdmissionOutlivesDispatchWatchdog" {
				dispatchWatchdogUsesObservation = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if fn.Name.Name != "waitForSpawnedChildTrigger" && sel.Sel.Name == "FindSpawnedChildTriggerID" {
				t.Errorf("%s polls FindSpawnedChildTriggerID directly at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			pkg, ok := sel.X.(*ast.Ident)
			if fn.Name.Name == "observeAdmissionWaitBeyondDispatchTimeout" && ok && pkg.Name == "time" && sel.Sel.Name == "NewTimer" && len(call.Args) == 1 {
				add, added := call.Args[0].(*ast.BinaryExpr)
				if added {
					left, leftOK := add.X.(*ast.Ident)
					margin, marginOK := add.Y.(*ast.BinaryExpr)
					if marginOK {
						amount, amountOK := margin.X.(*ast.BasicLit)
						unit, unitOK := margin.Y.(*ast.SelectorExpr)
						if unitOK {
							unitPkg, unitPkgOK := unit.X.(*ast.Ident)
							dispatchWatchdogSpansTimeout = add.Op == token.ADD && leftOK && left.Name == "dispatchTimeout" &&
								margin.Op == token.MUL && amountOK && amount.Value == "100" && unitPkgOK &&
								unitPkg.Name == "time" && unit.Sel.Name == "Millisecond"
						}
					}
				}
			}
			if fn.Name.Name == "observeAdmissionWaitBeyondDispatchTimeout" && ok && pkg.Name == "orchestrator" && sel.Sel.Name == "AdmissionWaitActiveForTest" {
				dispatchWatchdogObservesAdmissionWait = true
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused" && ok && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "ProgressTimeoutPausedForTest":
					cancellationChecksPausedController = true
				case "ExpireProgressTimeoutForTest":
					cancellationRejectsPausedExpiry = true
				}
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitNoProgressTimeoutResumesAfterAdmissionWait" && ok && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "ProgressTimeoutPausedForTest":
					noProgressChecksPausedController = true
				case "ForceProgressTimeoutForTest":
					noProgressRejectsPausedExpiry = true
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
				case "SetNodeTimeoutRemainingForTest":
					remainingBudgetControlsRemainder = true
					remainingBudgetSetPos = call.Pos()
				}
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline" && ok && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "NodeTimeoutPausedForTest":
					earlyResumeChecksPausedController = true
				case "ForceNodeTimeoutForTest":
					earlyResumeRejectsPausedExpiry = true
				case "SetNodeTimeoutRemainingForTest":
					earlyResumeControlsRemainder = true
					earlyResumeSetPos = call.Pos()
				}
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait" && ok && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "NodeTimeoutPausedForTest":
					multiKeyChecksPausedController = true
				case "ForceNodeTimeoutForTest":
					multiKeyRejectsPausedExpiry = true
				case "NodeTimeoutStateForTest":
					multiKeyInspectsControllerState = true
				case "SetNodeTimeoutRemainingForTest":
					multiKeyControlsRemainder = true
				}
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget" && sel.Sel.Name == "release" {
				if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "gate" {
					remainingBudgetReleasePos = call.Pos()
				}
			}
			if fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline" && sel.Sel.Name == "release" {
				if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "gate" {
					earlyResumeReleasePos = call.Pos()
				}
			}
			isPollingHelper := fn.Name.Name == "waitForConcurrencyHolder" || fn.Name.Name == "waitForNodeTimeoutPaused" || fn.Name.Name == "waitForNodeTimeoutResumed" || fn.Name.Name == "waitForProgressTimeoutResumed" || fn.Name.Name == "observeAdmissionWaitBeyondDispatchTimeout" || fn.Name.Name == "waitForPlanAdmissionWaiter" || fn.Name.Name == "waitForSpawnedChildTrigger"
			isCancellationRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitParentCancellationWhileAdmissionTimeoutPaused"
			isRemainingBudgetRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget"
			isEarlyResumeRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline"
			isNoProgressRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitNoProgressTimeoutResumesAfterAdmissionWait"
			isDispatchWatchdogRegression := fn.Name.Name == "testRunAndAwaitAdmissionOutlivesDispatchWatchdog"
			isMultiKeyRegression := fn.Name.Name == "TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait"
			if (isPollingHelper || isCancellationRegression || isRemainingBudgetRegression || isEarlyResumeRegression || isNoProgressRegression || isDispatchWatchdogRegression || isMultiKeyRegression) && sel.Sel.Name == "Sleep" && ok && pkg.Name == "time" {
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
		isHelper := fn.Name.Name == "waitForConcurrencyHolder" || fn.Name.Name == "waitForNodeTimeoutPaused" || fn.Name.Name == "waitForNodeTimeoutResumed" || fn.Name.Name == "waitForProgressTimeoutResumed" || fn.Name.Name == "observeAdmissionWaitBeyondDispatchTimeout" || fn.Name.Name == "waitForPlanAdmissionWaiter" || fn.Name.Name == "waitForSpawnedChildTrigger"
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
	if !remainingBudgetControlsRemainder {
		t.Error("remaining-budget regression does not establish its pre-admission timeout remainder")
	}
	if !cancellationGateUngated {
		t.Error("parent-cancellation regression must not block its action-start signal")
	}
	if !remainingBudgetGateGated {
		t.Error("remaining-budget regression does not gate action progress while setting the timeout remainder")
	}
	if remainingBudgetSetPos == token.NoPos || remainingBudgetReleasePos == token.NoPos || remainingBudgetSpawnWaitPos == token.NoPos ||
		!(remainingBudgetSetPos < remainingBudgetReleasePos && remainingBudgetReleasePos < remainingBudgetSpawnWaitPos) {
		t.Error("remaining-budget regression must set the remainder, release the action, then wait for child admission")
	}
	if !earlyResumeChecksPausedController {
		t.Error("early-resume regression does not inspect the paused timeout controller")
	}
	if !earlyResumeRejectsPausedExpiry {
		t.Error("early-resume regression does not reject forced timeout while admission is paused")
	}
	if !earlyResumeControlsRemainder {
		t.Error("early-resume regression does not establish its pre-admission timeout remainder")
	}
	if !earlyResumeGateGated {
		t.Error("early-resume regression does not gate action progress while setting the timeout remainder")
	}
	if earlyResumeSetPos == token.NoPos || earlyResumeReleasePos == token.NoPos || earlyResumeSpawnWaitPos == token.NoPos ||
		!(earlyResumeSetPos < earlyResumeReleasePos && earlyResumeReleasePos < earlyResumeSpawnWaitPos) {
		t.Error("early-resume regression must set the remainder, release the action, then wait for child admission")
	}
	if !noProgressChecksPausedController {
		t.Error("no-progress admission regression does not inspect the paused timeout controller")
	}
	if !noProgressRejectsPausedExpiry {
		t.Error("no-progress admission regression does not reject forced timeout while admission is paused")
	}
	if !dispatchWatchdogObservesAdmissionWait {
		t.Error("dispatch-watchdog regression does not observe the real admission wait state")
	}
	if !dispatchWatchdogUsesObservation {
		t.Error("dispatch-watchdog regression does not delegate its observation window")
	}
	if !dispatchWatchdogSpansTimeout {
		t.Error("dispatch-watchdog observation does not span the configured timeout")
	}
	if !multiKeyChecksPausedController || !multiKeyRejectsPausedExpiry || !multiKeyInspectsControllerState || !multiKeyControlsRemainder {
		t.Error("multi-key admission regression does not control and inspect its paused timeout state")
	}
	if !multiKeyGateGated {
		t.Error("multi-key admission regression does not gate action progress while setting the timeout remainder")
	}
}
