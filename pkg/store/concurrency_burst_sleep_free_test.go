package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestConcurrencyBurstRegressionDoesNotSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "concurrency_burst_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse concurrency_burst_test.go: %v", err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestConcurrency_BurstConcurrentAcquireAndRelease" || fn.Body == nil {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
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
				t.Errorf("burst regression contains time.Sleep at %s", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("TestConcurrency_BurstConcurrentAcquireAndRelease not found")
	}
}
