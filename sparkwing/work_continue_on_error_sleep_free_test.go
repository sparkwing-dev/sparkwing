package sparkwing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestWorkFailureHandlingRegressionsDoNotSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_continue_on_error_test.go", nil, 0)
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
			t.Errorf("%s: synchronize sibling entry directly", fset.Position(call.Pos()))
		}
		return true
	})
}

func TestDefaultFailFastObservesSiblingCancellationDirectly(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_continue_on_error_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "TestRunWork_DefaultFailFastCancelsSiblings" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("TestRunWork_DefaultFailFastCancelsSiblings declaration missing")
	}
	observesCancellation := false
	ast.Inspect(target.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, pkgOK := sel.X.(*ast.Ident)
			if pkgOK && pkg.Name == "time" && sel.Sel.Name == "After" {
				t.Errorf("%s: observe sibling cancellation directly", fset.Position(node.Pos()))
			}
		case *ast.UnaryExpr:
			call, ok := node.X.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || len(call.Args) != 0 {
				return true
			}
			receiver, receiverOK := sel.X.(*ast.Ident)
			if node.Op == token.ARROW && receiverOK && receiver.Name == "ctx" && sel.Sel.Name == "Done" {
				observesCancellation = true
			}
		}
		return true
	})
	if !observesCancellation {
		t.Fatal("default fail-fast regression must receive from ctx.Done")
	}
}
