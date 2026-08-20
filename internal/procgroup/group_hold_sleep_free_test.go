//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestGroupHelperIndefiniteHoldsDoNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse group_test.go: %v", err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestGroupHelperProcess" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, pkgOK := sel.X.(*ast.Ident)
			if !pkgOK || pkg.Name != "time" || sel.Sel.Name != "Sleep" {
				return true
			}
			mul, ok := call.Args[0].(*ast.BinaryExpr)
			if !ok {
				return true
			}
			amount, amountOK := mul.X.(*ast.BasicLit)
			unit, unitOK := mul.Y.(*ast.SelectorExpr)
			if !unitOK {
				return true
			}
			unitPkg, unitPkgOK := unit.X.(*ast.Ident)
			if amountOK && unitPkgOK && amount.Value == "30" && unitPkg.Name == "time" && unit.Sel.Name == "Second" {
				t.Errorf("TestGroupHelperProcess contains a 30-second hold at %s", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("group_test.go does not declare TestGroupHelperProcess")
	}
}
