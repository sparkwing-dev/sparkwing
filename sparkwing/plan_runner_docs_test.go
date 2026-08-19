package sparkwing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestPrefersDocumentationDoesNotClaimProfileDefault(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "plan.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Prefers" {
			continue
		}
		if fn.Doc == nil {
			t.Fatal("Prefers has no documentation")
		}
		if strings.Contains(strings.ToLower(fn.Doc.Text()), "profile default") {
			t.Fatal("Prefers documentation advertises the removed profile default")
		}
		return
	}
	t.Fatal("Prefers method not found")
}
