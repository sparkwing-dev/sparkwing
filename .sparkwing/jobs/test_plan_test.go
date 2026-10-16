package jobs

import (
	"bytes"
	"context"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
)

func TestTestPipelineMeasuresAndBoundsItsCPU(t *testing.T) {
	plan := inspectPlan(t, func(plan *sparkwing.Plan) error {
		return (&Test{}).Plan(context.Background(), plan, TestArgs{}, sparkwing.RunContext{Pipeline: "test"})
	})

	if hints := plan.Resources; hints != nil {
		t.Fatalf("resource pin = %#v, want measured admission", hints)
	}
	nodes := plan.Jobs
	if len(nodes) != 1 {
		t.Fatalf("planned nodes = %d, want 1", len(nodes))
	}
	if hints := nodes[0].Resources; hints != nil {
		t.Fatalf("node resource pin = %#v, want measured admission", hints)
	}
	if got := testGoCommand(hostShape{cpus: 14}); got != "GOMAXPROCS=6 go test -p 6 -timeout 25m ./..." {
		t.Fatalf("bounded command = %q", got)
	}
	if got := testGoCommand(hostShape{cpus: 4, singleTenant: true}); got != "GOMAXPROCS=1 go test -p 2 -timeout 25m ./..." {
		t.Fatalf("hosted four-core command = %q", got)
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
	want := `func (p *Test) run(cmd string) func(context.Context) error {
	return func(ctx context.Context) error {
		return withProductTestHome(func(home string) error {
			if _, err := sparkwing.Bash(ctx, productTestScript(cmd, home)).Run(); err != nil {
				return err
			}
			sparkwing.Info(ctx, "go test: all packages passed")
			return nil
		})
	}
}`
	if formatted.String() != want {
		t.Fatalf("Test.run must execute only the bounded test command, scrubbed of the node's "+
			"ambient service bindings; got:\n%s", formatted.String())
	}
}
