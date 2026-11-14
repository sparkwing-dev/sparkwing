package runstate_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

// The CLI's --status values come from RunStates, so a RunState constant
// missing from it would be refused by every run filter.
func TestRunStatesListsEveryDeclaredRunState(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	fset := token.NewFileSet()
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			for _, spec := range gen.Specs {
				value := spec.(*ast.ValueSpec)
				if ident, ok := value.Type.(*ast.Ident); !ok || ident.Name != "RunState" {
					continue
				}
				for _, literal := range value.Values {
					text, err := strconv.Unquote(literal.(*ast.BasicLit).Value)
					if err != nil {
						t.Fatal(err)
					}
					declared = append(declared, text)
				}
			}
		}
	}
	var listed []string
	for _, state := range runstate.RunStates() {
		listed = append(listed, string(state))
	}
	slices.Sort(declared)
	slices.Sort(listed)
	if len(declared) == 0 || !slices.Equal(declared, listed) {
		t.Fatalf("RunStates() = %v, declared RunState constants = %v", listed, declared)
	}
}
