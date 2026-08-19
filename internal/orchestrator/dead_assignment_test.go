package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestProductionCodeHasNoStandaloneIdentifierDiscards(t *testing.T) {
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
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			assign, ok := node.(*ast.AssignStmt)
			if !ok || assign.Tok != token.ASSIGN || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
				return true
			}
			lhs, lhsOK := assign.Lhs[0].(*ast.Ident)
			_, rhsOK := assign.Rhs[0].(*ast.Ident)
			if lhsOK && lhs.Name == "_" && rhsOK {
				t.Errorf("%s discards an identifier with a standalone assignment", fset.Position(assign.Pos()))
			}
			return true
		})
	}
}
