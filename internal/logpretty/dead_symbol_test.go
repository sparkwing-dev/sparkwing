package logpretty

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestProductionDoesNotRetainSymbolsThroughBlankVariables(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pretty.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range values.Names {
				if name.Name == "_" {
					t.Errorf("production blank variable at %s retains an otherwise unused symbol", fset.Position(name.Pos()))
				}
			}
		}
	}
}
