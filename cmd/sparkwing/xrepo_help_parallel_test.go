package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestXrepoRuntimeHelpRunsInParallel(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "xrepo_help_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestXrepoRuntimeHelpUsesCommandRegistry" {
			continue
		}
		found = true
		if !firstStatementIsParallel(fn) {
			t.Fatal("TestXrepoRuntimeHelpUsesCommandRegistry must call t.Parallel() first")
		}
	}
	if !found {
		t.Fatal("TestXrepoRuntimeHelpUsesCommandRegistry declaration is missing")
	}
}

func firstStatementIsParallel(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	expr, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	receiver, ok := sel.X.(*ast.Ident)
	return ok && receiver.Name == "t"
}
