package jobs

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTestPipelineReservesAndBoundsItsCPU(t *testing.T) {
	plan := sparkwing.NewPlan()
	if err := (&Test{}).Plan(context.Background(), plan, sparkwing.NoInputs{}, sparkwing.RunContext{Pipeline: "test"}); err != nil {
		t.Fatal(err)
	}

	wantCores := float64(preCommitCPUReservation(runtime.NumCPU()))
	if hints := plan.ResourceHints(); hints == nil || hints.Cores != wantCores {
		t.Fatalf("reserved cores = %#v, want %.0f", hints, wantCores)
	}
	if got := testGoCommand(14); got != "GOMAXPROCS=6 go test -p 6 ./..." {
		t.Fatalf("bounded command = %q", got)
	}

	file, err := parser.ParseFile(token.NewFileSet(), "test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	usesBoundedCommand := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" || fn.Recv == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || !isSelector(call.Fun, "sparkwing", "Bash") || len(call.Args) != 2 {
				return true
			}
			bounded, ok := call.Args[1].(*ast.CallExpr)
			usesBoundedCommand = ok && isIdent(bounded.Fun, "testGoCommand") && len(bounded.Args) == 1 && isCall(bounded.Args[0], "runtime", "NumCPU")
			return true
		})
	}
	if !usesBoundedCommand {
		t.Fatal("Test.run must pass testGoCommand(runtime.NumCPU()) to sparkwing.Bash")
	}
}

func isSelector(expr ast.Expr, receiver, name string) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	return ok && isIdent(selector.X, receiver) && selector.Sel.Name == name
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func isCall(expr ast.Expr, receiver, name string) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && len(call.Args) == 0 && isSelector(call.Fun, receiver, name)
}
