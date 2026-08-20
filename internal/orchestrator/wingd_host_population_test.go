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
		exactReturnPredicate := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if clause, ok := node.(*ast.CaseClause); ok && fn.Name.Name == "waitForDegradedConcurrencyPopulation" {
				exactReturnPredicate = exactReturnPredicate || isExactPopulationReturnCase(clause)
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
		if fn.Name.Name == "waitForDegradedConcurrencyPopulation" && !exactReturnPredicate {
			t.Error("population helper must require exactly one holder and one waiter")
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s not found", name)
		}
	}
}

func isExactPopulationReturnCase(clause *ast.CaseClause) bool {
	if len(clause.List) != 1 || len(clause.Body) != 1 {
		return false
	}
	if _, ok := clause.Body[0].(*ast.ReturnStmt); !ok {
		return false
	}
	var leaves []ast.Expr
	collectAndLeaves(clause.List[0], &leaves)
	if len(leaves) != 3 {
		return false
	}
	errNil, holdersOne, waitersOne := 0, 0, 0
	for _, leaf := range leaves {
		binary, ok := leaf.(*ast.BinaryExpr)
		if !ok {
			return false
		}
		if isIdentEqualNil(binary, "err") {
			errNil++
		} else if isStateCountOne(binary, "Holders") {
			holdersOne++
		} else if isStateCountOne(binary, "Waiters") {
			waitersOne++
		} else {
			return false
		}
	}
	return errNil == 1 && holdersOne == 1 && waitersOne == 1
}

func collectAndLeaves(expr ast.Expr, leaves *[]ast.Expr) {
	if paren, ok := expr.(*ast.ParenExpr); ok {
		collectAndLeaves(paren.X, leaves)
		return
	}
	if binary, ok := expr.(*ast.BinaryExpr); ok && binary.Op == token.LAND {
		collectAndLeaves(binary.X, leaves)
		collectAndLeaves(binary.Y, leaves)
		return
	}
	*leaves = append(*leaves, expr)
}

func isIdentEqualNil(binary *ast.BinaryExpr, name string) bool {
	if binary.Op != token.EQL {
		return false
	}
	left, leftOK := binary.X.(*ast.Ident)
	right, rightOK := binary.Y.(*ast.Ident)
	return leftOK && left.Name == name && rightOK && right.Name == "nil"
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
