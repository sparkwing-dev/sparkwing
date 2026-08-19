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
		doc := strings.ToLower(fn.Doc.Text())
		for _, retired := range []string{"profile default", "default runner"} {
			if strings.Contains(doc, retired) {
				t.Fatalf("Prefers documentation advertises removed %q selection", retired)
			}
		}
		if !strings.Contains(doc, "do not affect runner selection") {
			t.Fatal("Prefers documentation does not state its metadata-only behavior")
		}
		return
	}
	t.Fatal("Prefers method not found")
}
