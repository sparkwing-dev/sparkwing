package logpretty

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProductionDoesNotRetainSymbolsThroughUntypedBlankVariables(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Clean(name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, pos := range untypedBlankIdentifierRetentions(file) {
			t.Errorf("production blank variable at %s retains an otherwise unused symbol", fset.Position(pos))
		}
	}
}

func TestUntypedBlankIdentifierRetentionClassification(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{name: "untyped identifier", src: "package p; var _ = unused", want: 1},
		{name: "typed interface assertion", src: "package p; var _ interface{ Write([]byte) (int, error) } = (*writer)(nil)"},
		{name: "discarded call result", src: "package p; var _ = register()"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", tt.src, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(untypedBlankIdentifierRetentions(file)); got != tt.want {
				t.Fatalf("retentions = %d, want %d", got, tt.want)
			}
		})
	}
}

func untypedBlankIdentifierRetentions(file *ast.File) []token.Pos {
	var positions []token.Pos
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok || values.Type != nil || len(values.Names) != len(values.Values) {
				continue
			}
			for i, name := range values.Names {
				if _, ok := values.Values[i].(*ast.Ident); name.Name == "_" && ok {
					positions = append(positions, name.Pos())
				}
			}
		}
	}
	return positions
}
