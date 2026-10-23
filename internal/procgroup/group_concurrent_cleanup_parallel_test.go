//go:build !windows

package procgroup

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"
)

func TestConcurrentCleanupStressRunsIsolatedIterationsInParallel(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
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
		if rangeRunsParallelSubtest(fset, loop) {
			return
		}
	}
	t.Fatal("concurrent cleanup stress must run each isolated iteration as a parallel subtest")
}

func rangeRunsParallelSubtest(fset *token.FileSet, loop *ast.RangeStmt) bool {
	if len(loop.Body.List) != 1 {
		return false
	}
	expr, ok := loop.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	const expected = `t.Run(fmt.Sprintf("iteration-%d", iteration), func(t *testing.T) {
	t.Parallel()
	g := startConcurrentCleanupHelper(t)
	results := make(chan error, 2)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		results <- g.Finish(ctx, 10*time.Millisecond)
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		results <- g.Terminate(ctx, 10*time.Millisecond)
	}()
	for range 2 {
		if err := <-results; errors.Is(err, ErrCleanup) {
			t.Fatalf("completed concurrent cleanup reported failure: %v", err)
		}
	}
	if !g.Reaped() {
		t.Fatalf("group %d was not reaped", g.ID())
	}
})`
	var rendered bytes.Buffer
	return format.Node(&rendered, fset, expr.X) == nil && rendered.String() == expected
}
