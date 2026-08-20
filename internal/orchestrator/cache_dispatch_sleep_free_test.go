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
		usesSpawnWait := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForSpawnedChildTrigger" {
				usesSpawnWait = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if fn.Name.Name != "waitForSpawnedChildTrigger" && sel.Sel.Name == "FindSpawnedChildTriggerID" {
				t.Errorf("%s polls FindSpawnedChildTriggerID directly at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			pkg, ok := sel.X.(*ast.Ident)
			if fn.Name.Name == "waitForSpawnedChildTrigger" && sel.Sel.Name == "Sleep" && ok && pkg.Name == "time" {
				t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
		if fn.Name.Name != "waitForConcurrencyHolder" && fn.Name.Name != "waitForSpawnedChildTrigger" && !usesSpawnWait {
			t.Errorf("%s does not use waitForSpawnedChildTrigger", fn.Name.Name)
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_dispatch_test.go does not declare %s", name)
		}
	}
}
