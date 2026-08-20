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
		"TestConcurrency_QueueSerializesConcurrentHolders":       false,
		"TestConcurrency_QueueSerializesAcrossRuns":              false,
		"TestConcurrency_PlanLevelQueueSerializesConcurrentRuns": false,
		"TestConcurrency_PlanLevelQueueEmitsAdmissionEvents":     false,
	}
	usesGate := map[string]bool{}
	usesPopulation := map[string]bool{}
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
			if lit, ok := node.(*ast.CompositeLit); ok {
				if ident, ok := lit.Type.(*ast.Ident); ok && ident.Name == "cacheStepGate" {
					usesGate[fn.Name.Name] = true
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForCacheConcurrencyPopulation" {
				usesPopulation[fn.Name.Name] = true
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
		if len(name) > 4 && name[:4] == "Test" && (!usesGate[name] || !usesPopulation[name]) {
			t.Errorf("%s does not gate execution on an observed concurrency population", name)
		}
	}
}
