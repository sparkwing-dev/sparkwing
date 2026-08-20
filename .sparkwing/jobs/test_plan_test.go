package jobs

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
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
	var runDecl *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" || fn.Recv == nil {
			continue
		}
		runDecl = fn
	}
	if runDecl == nil {
		t.Fatal("Test.run declaration not found")
	}
	var formatted bytes.Buffer
	if err := format.Node(&formatted, token.NewFileSet(), runDecl); err != nil {
		t.Fatal(err)
	}
	want := `func (p *Test) run(ctx context.Context) error {
	if err := withGoTestScratch(func(testRoot string) error {
		_, err := sparkwing.Bash(ctx, testGoCommand(runtime.NumCPU())).Env("TMPDIR", testRoot).Run()
		return err
	}); err != nil {
		return err
	}
	sparkwing.Info(ctx, "go test: all packages passed")
	return nil
}`
	if formatted.String() != want {
		t.Fatalf("Test.run must execute only the bounded test command; got:\n%s", formatted.String())
	}
}
