package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCacheLeaderReadinessDoesNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"TestConcurrency_PlanLevelSkipShortCircuits": false,
		"testCancelOthersStopsLeader":                false,
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
		usesLeaderWait := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForLeaderHolding" {
				usesLeaderWait = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
				t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
		if !usesLeaderWait {
			t.Errorf("%s does not use waitForLeaderHolding", fn.Name.Name)
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_dispatch_test.go does not declare %s", name)
		}
	}
}
