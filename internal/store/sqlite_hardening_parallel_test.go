package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestIndependentSQLiteWriterRegressionsRunInParallel(t *testing.T) {
	targets := map[string]bool{
		"TestConcurrentWriters_FailPolicyNoBusyError":  false,
		"TestConcurrentWriters_QueuePolicyNoBusyError": false,
	}
	file, err := parser.ParseFile(token.NewFileSet(), "sqlite_hardening_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = sqliteFirstStatementIsParallel(fn)
	}
	for name, parallel := range targets {
		if !parallel {
			t.Errorf("%s must call t.Parallel() as its first statement", name)
		}
	}
}

func sqliteFirstStatementIsParallel(fn *ast.FuncDecl) bool {
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
