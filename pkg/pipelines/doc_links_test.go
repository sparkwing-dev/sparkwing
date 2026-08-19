package pipelines

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestPackageDocumentationLinksResolve(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}

	declared := map[string]bool{}
	var packageDoc string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		if name == "doc.go" && file.Doc != nil {
			packageDoc = file.Doc.Text()
		}
		for _, decl := range file.Decls {
			switch node := decl.(type) {
			case *ast.FuncDecl:
				if node.Recv == nil {
					declared[node.Name.Name] = true
				}
			case *ast.GenDecl:
				for _, spec := range node.Specs {
					switch item := spec.(type) {
					case *ast.TypeSpec:
						declared[item.Name.Name] = true
					case *ast.ValueSpec:
						for _, ident := range item.Names {
							declared[ident.Name] = true
						}
					}
				}
			}
		}
	}
	if packageDoc == "" {
		t.Fatal("package documentation not found")
	}

	links := regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_]*)\]`)
	for _, match := range links.FindAllStringSubmatch(packageDoc, -1) {
		if !declared[match[1]] {
			t.Errorf("package documentation links undefined identifier %s", match[1])
		}
	}
}
