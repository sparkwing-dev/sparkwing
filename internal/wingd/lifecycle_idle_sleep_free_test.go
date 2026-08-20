package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestIdleHolderRegressionDoesNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"TestIdleExit_WaitsForHolders": false,
		"observeHolderFor":             false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "lifecycle_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse lifecycle_test.go: %v", err)
	}
	usesObservation := false
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
			if fn.Name.Name == "TestIdleExit_WaitsForHolders" && len(call.Args) == 5 {
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "observeHolderFor" {
					duration, durationOK := call.Args[4].(*ast.BinaryExpr)
					if durationOK && duration.Op == token.ADD {
						idleTimeout, idleOK := duration.X.(*ast.Ident)
						margin, marginOK := duration.Y.(*ast.BinaryExpr)
						if !idleOK || idleTimeout.Name != "idleTimeout" || !marginOK || margin.Op != token.MUL {
							return true
						}
						amount, amountOK := margin.X.(*ast.BasicLit)
						unit, unitOK := margin.Y.(*ast.SelectorExpr)
						if unitOK {
							pkg, pkgOK := unit.X.(*ast.Ident)
							usesObservation = amountOK && amount.Value == "100" && unit.Sel.Name == "Millisecond" && pkgOK && pkg.Name == "time"
						}
					}
				}
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" {
				t.Errorf("%s uses time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s declaration not found", name)
		}
	}
	if !usesObservation {
		t.Error("TestIdleExit_WaitsForHolders must observe the active holder")
	}
}
