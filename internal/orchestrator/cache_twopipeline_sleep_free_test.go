package orchestrator_test

import (
	"go/ast"
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
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if ident, isIdent := sel.X.(*ast.Ident); ok && isIdent && ident.Name == "time" {
				t.Errorf("unsharedStep contains time.%s at %s", sel.Sel.Name, fset.Position(sel.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("unsharedStep declaration not found")
	}
}
