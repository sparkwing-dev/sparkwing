package pipelines

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
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

func TestPackageDocumentationPipelineShapeMatchesType(t *testing.T) {
	raw, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "Each Pipeline carries"
	start := strings.Index(string(raw), prefix)
	if start < 0 {
		t.Fatal("package documentation does not describe Pipeline shape")
	}
	claim := string(raw)[start:]
	if end := strings.IndexByte(claim, '.'); end >= 0 {
		claim = claim[:end]
	}

	fieldTypes := map[string]bool{}
	typ := reflect.TypeOf(Pipeline{})
	for i := 0; i < typ.NumField(); i++ {
		fieldType := typ.Field(i).Type
		for fieldType.Kind() == reflect.Pointer || fieldType.Kind() == reflect.Slice || fieldType.Kind() == reflect.Map {
			fieldType = fieldType.Elem()
		}
		if fieldType.Name() != "" {
			fieldTypes[fieldType.Name()] = true
		}
	}

	links := regexp.MustCompile(`\[([A-Za-z_][A-Za-z0-9_]*)\]`)
	for _, match := range links.FindAllStringSubmatch(claim, -1) {
		if !fieldTypes[match[1]] {
			t.Errorf("package documentation says Pipeline carries %s, but Pipeline has no field of that type", match[1])
		}
	}
}
