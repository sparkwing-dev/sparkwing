package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCacheStepSerializationDoesNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"cacheStep":                         false,
		"waitForCacheConcurrencyPopulation": false,
		"waitForCacheEvent":                 false,
		"TestConcurrency_QueueSerializesConcurrentHolders":                             false,
		"TestConcurrency_QueueSerializesAcrossRuns":                                    false,
		"TestConcurrency_PlanLevelQueueSerializesConcurrentRuns":                       false,
		"TestConcurrency_PlanLevelQueueEmitsAdmissionEvents":                           false,
		"TestConcurrency_RunAndAwaitParentTimeoutResumesWithRemainingBudget":           false,
		"TestConcurrency_RunAndAwaitParentTimeoutPausesBeforeDeadline":                 false,
		"TestConcurrency_RunAndAwaitParentTimeoutCountsMissedPromotionAsAdmissionWait": false,
		"TestConcurrency_RunAndAwaitParentTimeoutAggregatesMultiKeyAdmissionWait":      false,
	}
	usesGate := map[string]bool{}
	usesPopulation := map[string]bool{}
	populationOwners := map[string]bool{
		"TestConcurrency_QueueSerializesConcurrentHolders":       true,
		"TestConcurrency_QueueSerializesAcrossRuns":              true,
		"TestConcurrency_PlanLevelQueueSerializesConcurrentRuns": true,
		"TestConcurrency_PlanLevelQueueEmitsAdmissionEvents":     true,
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
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForCacheConcurrencyPopulation" {
				usesPopulation[fn.Name.Name] = true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "installCacheStepGate" {
				usesGate[fn.Name.Name] = true
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sleep" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
					t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_dispatch_test.go does not declare %s", name)
		}
		if len(name) > 4 && name[:4] == "Test" && !usesGate[name] {
			t.Errorf("%s does not gate cache-step execution", name)
		}
		if populationOwners[name] && !usesPopulation[name] {
			t.Errorf("%s does not observe its concurrency population", name)
		}
	}
}
