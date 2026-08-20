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
		waitsForPopulation := false
		holdersOne, waitersOne := false, false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if binary, ok := node.(*ast.BinaryExpr); ok && fn.Name.Name == "waitForDegradedConcurrencyPopulation" {
				holdersOne = holdersOne || isStateCountOne(binary, "Holders")
				waitersOne = waitersOne || isStateCountOne(binary, "Waiters")
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch fun := call.Fun.(type) {
			case *ast.Ident:
				if fn.Name.Name == "TestRun_DegradedConcurrencyGroupsStillSerialize" &&
					fun.Name == "waitForDegradedConcurrencyPopulation" && len(call.Args) == 4 {
					waitsForPopulation = isBoxScopeKey(call.Args[3])
				}
			case *ast.SelectorExpr:
				pkg, ok := fun.X.(*ast.Ident)
				if ok && pkg.Name == "time" &&
					(fun.Sel.Name == "After" || fun.Sel.Name == "Sleep") {
					t.Errorf("%s uses time.%s at %s", fn.Name.Name, fun.Sel.Name, fset.Position(call.Pos()))
				}
			}
			return true
		})
		if fn.Name.Name == "TestRun_DegradedConcurrencyGroupsStillSerialize" && !waitsForPopulation {
			t.Error("serialization regression does not wait for persisted holder/waiter population")
		}
		if fn.Name.Name == "waitForDegradedConcurrencyPopulation" && (!holdersOne || !waitersOne) {
			t.Error("population helper must require exactly one holder and one waiter")
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s not found", name)
		}
	}
}

func isStateCountOne(binary *ast.BinaryExpr, field string) bool {
	if binary.Op != token.EQL {
		return false
	}
	one, ok := binary.Y.(*ast.BasicLit)
	if !ok || one.Value != "1" {
		return false
	}
	call, ok := binary.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok || name.Name != "len" {
		return false
	}
	sel, ok := call.Args[0].(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != field {
		return false
	}
	state, ok := sel.X.(*ast.Ident)
	return ok && state.Name == "state"
}

func isBoxScopeKey(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok || name.Name != "scopedGroupKey" {
		return false
	}
	group, groupOK := call.Args[0].(*ast.Ident)
	runID, runOK := call.Args[1].(*ast.BasicLit)
	return groupOK && group.Name == "boxGroup" && runOK && runID.Value == `""`
}
