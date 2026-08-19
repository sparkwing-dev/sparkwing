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
	declared := productionIdentifiers(t)
	file, err := parser.ParseFile(token.NewFileSet(), "doc.go", nil, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	packageDoc := ""
	if file.Doc != nil {
		packageDoc = file.Doc.Text()
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

func TestExampleDocumentationLinksResolve(t *testing.T) {
	raw, err := os.ReadFile("example_test.go")
	if err != nil {
		t.Fatal(err)
	}
	declared := productionIdentifiers(t)
	links := regexp.MustCompile(`\[pipelines\.([A-Za-z_][A-Za-z0-9_]*)\]`)
	for _, match := range links.FindAllStringSubmatch(string(raw), -1) {
		if !declared[match[1]] {
			t.Errorf("example documentation links undefined identifier pipelines.%s", match[1])
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

func TestPackageDocumentationTriggerShapeMatchesType(t *testing.T) {
	raw, err := os.ReadFile("doc.go")
	if err != nil {
		t.Fatal(err)
	}
	const prefix = "Triggers fan out by source:"
	doc := strings.Join(strings.Fields(strings.ReplaceAll(string(raw), "//", " ")), " ")
	start := strings.Index(doc, prefix)
	if start < 0 {
		t.Fatal("package documentation does not describe trigger sources")
	}
	claim := doc[start:]
	if end := strings.IndexByte(claim, '.'); end >= 0 {
		claim = claim[:end]
	}

	typ := reflect.TypeOf(Triggers{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		fieldType := field.Type
		for fieldType.Kind() == reflect.Pointer {
			fieldType = fieldType.Elem()
		}
		if fieldType.Name() != "" && fieldType.PkgPath() != "" {
			if !strings.Contains(claim, "["+fieldType.Name()+"]") {
				t.Errorf("package documentation omits trigger source %s", field.Name)
			}
			continue
		}
		if !strings.Contains(strings.ToLower(claim), strings.ToLower(field.Name)) {
			t.Errorf("package documentation omits trigger source %s", field.Name)
		}
	}
}

func productionIdentifiers(t *testing.T) map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
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
	return declared
}
