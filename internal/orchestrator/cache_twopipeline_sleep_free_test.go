package orchestrator_test

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"
)

func TestUnsharedCacheFixtureStepDoesNotWait(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cache_twopipeline_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cache_twopipeline_test.go: %v", err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "unsharedStep" {
			continue
		}
		found = true
		var rendered bytes.Buffer
		if err := format.Node(&rendered, fset, fn); err != nil {
			t.Fatalf("format unsharedStep: %v", err)
		}
		const want = "func unsharedStep() func(context.Context) error {\n\treturn func(context.Context) error { return nil }\n}"
		if rendered.String() != want {
			t.Errorf("unsharedStep must remain an inert closure; got:\n%s", rendered.String())
		}
	}
	if !found {
		t.Fatal("unsharedStep declaration not found")
	}
}
