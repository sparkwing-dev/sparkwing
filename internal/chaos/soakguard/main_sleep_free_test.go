//go:build !windows

package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSoakguardRegressionsDoNotSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Sleep" {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if ok && pkg.Name == "time" {
			t.Errorf("%s: synchronize helper processes explicitly", fset.Position(call.Pos()))
		}
		return true
	})
}
