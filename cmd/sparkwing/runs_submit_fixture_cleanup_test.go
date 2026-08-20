package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSubmitCLIFixtureOwnsTemporaryDirectory(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "runs_submit_process_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var recordsDirectory, cleansDirectory bool
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		switch fn.Name.Name {
		case "buildSubmitCLI":
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				assign, ok := node.(*ast.AssignStmt)
				if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
					return true
				}
				lhs, lok := assign.Lhs[0].(*ast.Ident)
				rhs, rok := assign.Rhs[0].(*ast.Ident)
				if lok && rok && lhs.Name == "submitCLIDir" && rhs.Name == "dir" {
					recordsDirectory = true
				}
				return true
			})
		case "TestMain":
			cleansDirectory = submitCLITestMainOwnsCleanup(fn)
		}
	}
	if !recordsDirectory {
		t.Fatal("buildSubmitCLI must retain the shared temporary directory for suite cleanup")
	}
	if !cleansDirectory {
		t.Fatal("TestMain must remove the shared CLI directory after the suite and preserve its exit code")
	}
}

func submitCLITestMainOwnsCleanup(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Body.List) != 3 {
		return false
	}
	param := fn.Type.Params.List[0]
	star, ok := param.Type.(*ast.StarExpr)
	if !ok || len(param.Names) != 1 || param.Names[0].Name != "m" {
		return false
	}
	typeName, ok := star.X.(*ast.SelectorExpr)
	if !ok || typeName.Sel.Name != "M" {
		return false
	}
	testingPkg, ok := typeName.X.(*ast.Ident)
	if !ok || testingPkg.Name != "testing" {
		return false
	}

	codeAssign, ok := fn.Body.List[0].(*ast.AssignStmt)
	if !ok || codeAssign.Tok != token.DEFINE || len(codeAssign.Lhs) != 1 || len(codeAssign.Rhs) != 1 {
		return false
	}
	code, ok := codeAssign.Lhs[0].(*ast.Ident)
	if !ok || code.Name != "code" || !isDirectCall(codeAssign.Rhs[0], "m", "Run", nil) {
		return false
	}
	remove, ok := fn.Body.List[1].(*ast.ExprStmt)
	if !ok || !isDirectCall(remove.X, "os", "RemoveAll", []string{"submitCLIDir"}) {
		return false
	}
	exit, ok := fn.Body.List[2].(*ast.ExprStmt)
	return ok && isDirectCall(exit.X, "os", "Exit", []string{"code"})
}

func isDirectCall(expr ast.Expr, receiver, method string, args []string) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok || len(call.Args) != len(args) {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != method {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok || recv.Name != receiver {
		return false
	}
	for i, name := range args {
		arg, ok := call.Args[i].(*ast.Ident)
		if !ok || arg.Name != name {
			return false
		}
	}
	return true
}
