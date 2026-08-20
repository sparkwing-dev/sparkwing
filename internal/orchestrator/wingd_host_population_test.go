package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDegradedBoxScopeSerializationUsesPersistedPopulation(t *testing.T) {
	targets := map[string]bool{
		"TestRun_DegradedConcurrencyGroupsStillSerialize": false,
		"waitForDegradedConcurrencyPopulation":            false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wingd_host_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse wingd_host_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
		if fn.Name.Name != "TestRun_DegradedConcurrencyGroupsStillSerialize" {
			continue
		}
		waitsForPopulation := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fun.Name == "waitForDegradedConcurrencyPopulation" {
					waitsForPopulation = true
				}
			case *ast.SelectorExpr:
				pkg, ok := fun.X.(*ast.Ident)
				if ok && pkg.Name == "time" && fun.Sel.Name == "After" {
					t.Errorf("serialization readiness uses time.After at %s", fset.Position(call.Pos()))
				}
			}
			return true
		})
		if !waitsForPopulation {
			t.Error("serialization regression does not wait for persisted holder/waiter population")
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s not found", name)
		}
	}
}
