//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestConcurrentCleanupStressRunsIsolatedIterationsInParallel(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "group_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "TestConcurrentFinishAndTerminateNeverLoseCompletedCleanup" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("TestConcurrentFinishAndTerminateNeverLoseCompletedCleanup declaration missing")
	}
	keepsCardinality := false
	ast.Inspect(target.Body, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "count" || len(spec.Values) != 1 {
			return true
		}
		value, ok := spec.Values[0].(*ast.BasicLit)
		keepsCardinality = ok && value.Kind == token.INT && value.Value == "50"
		return true
	})
	if !keepsCardinality {
		t.Fatal("concurrent cleanup stress must retain 50 iterations")
	}
	for _, stmt := range target.Body.List {
		loop, ok := stmt.(*ast.RangeStmt)
		if !ok {
			continue
		}
		over, ok := loop.X.(*ast.Ident)
		if !ok || over.Name != "count" {
			continue
		}
		if rangeRunsParallelSubtest(loop) {
			return
		}
	}
	t.Fatal("concurrent cleanup stress must run each isolated iteration as a parallel subtest")
}

func rangeRunsParallelSubtest(loop *ast.RangeStmt) bool {
	if len(loop.Body.List) != 1 {
		return false
	}
	expr, ok := loop.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	receiver, receiverOK := sel.X.(*ast.Ident)
	if !receiverOK || receiver.Name != "t" || sel.Sel.Name != "Run" {
		return false
	}
	worker, ok := call.Args[1].(*ast.FuncLit)
	if !ok || len(worker.Body.List) < 2 {
		return false
	}
	first, ok := worker.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	parallel, ok := first.X.(*ast.CallExpr)
	if !ok || len(parallel.Args) != 0 {
		return false
	}
	parallelSel, ok := parallel.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	parallelReceiver, receiverOK := parallelSel.X.(*ast.Ident)
	if !receiverOK || parallelReceiver.Name != "t" || parallelSel.Sel.Name != "Parallel" {
		return false
	}
	assign, ok := worker.Body.List[1].(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
		return false
	}
	group, ok := assign.Lhs[0].(*ast.Ident)
	starter, callOK := assign.Rhs[0].(*ast.CallExpr)
	if !ok || !callOK || group.Name != "g" || len(starter.Args) != 1 {
		return false
	}
	starterName, ok := starter.Fun.(*ast.Ident)
	starterArg, argOK := starter.Args[0].(*ast.Ident)
	return ok && argOK && starterName.Name == "startConcurrentCleanupHelper" && starterArg.Name == "t"
}
