package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestStopReleaseRoundsRunAsParallelSubtests(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "stop_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundTarget, foundParallelRounds := false, false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestStop_ReturnsOnlyAfterTheDaemonReleasedTheHome" || fn.Body == nil {
			continue
		}
		foundTarget = true
		if len(fn.Body.List) != 1 {
			continue
		}
		loop, ok := fn.Body.List[0].(*ast.ForStmt)
		if !ok || len(loop.Body.List) != 1 {
			continue
		}
		expr, ok := loop.Body.List[0].(*ast.ExprStmt)
		if !ok {
			continue
		}
		run, ok := expr.X.(*ast.CallExpr)
		if !ok || len(run.Args) != 2 {
			continue
		}
		sel, ok := run.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			continue
		}
		receiver, ok := sel.X.(*ast.Ident)
		callback, okCallback := run.Args[1].(*ast.FuncLit)
		if !ok || receiver.Name != "t" || !okCallback || len(callback.Body.List) == 0 {
			continue
		}
		first, ok := callback.Body.List[0].(*ast.ExprStmt)
		if !ok {
			continue
		}
		parallel, ok := first.X.(*ast.CallExpr)
		if !ok || len(parallel.Args) != 0 {
			continue
		}
		parallelSel, ok := parallel.Fun.(*ast.SelectorExpr)
		if !ok || parallelSel.Sel.Name != "Parallel" {
			continue
		}
		parallelReceiver, receiverOK := parallelSel.X.(*ast.Ident)
		foundParallelRounds = receiverOK && parallelReceiver.Name == "t"
	}
	if !foundTarget {
		t.Fatal("stop_test.go does not declare TestStop_ReturnsOnlyAfterTheDaemonReleasedTheHome")
	}
	if !foundParallelRounds {
		t.Fatal("stop release rounds must each run as a direct parallel subtest")
	}
}
