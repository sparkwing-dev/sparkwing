package sparkwing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func TestPrefersDocumentationStatesStoredBehavior(t *testing.T) {
	tests := []struct {
		file     string
		receiver string
	}{
		{file: "plan.go", receiver: "JobNode"},
		{file: "combinator.go", receiver: "JobGroup"},
	}
	for _, tt := range tests {
		t.Run(tt.receiver, func(t *testing.T) {
			doc := methodDoc(t, tt.file, tt.receiver, "Prefers")
			assertPreferenceContract(t, doc)
		})
	}
}

func TestPrefersStorageDocumentationStatesStoredBehavior(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "plan.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		typeDecl, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range typeDecl.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != "JobNode" {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				t.Fatal("JobNode is not a struct")
			}
			for _, field := range structType.Fields.List {
				if len(field.Names) == 1 && field.Names[0].Name == "prefers" {
					if field.Doc == nil {
						t.Fatal("JobNode.prefers has no documentation")
					}
					assertPreferenceContract(t, strings.ToLower(field.Doc.Text()))
					return
				}
			}
		}
	}
	t.Fatal("JobNode.prefers not found")
}

func TestExecutionModelIsClearlyHistorical(t *testing.T) {
	data, err := os.ReadFile("../DESIGN-execution-model.md")
	if err != nil {
		t.Fatal(err)
	}
	status := strings.ToLower(string(data))
	if !strings.Contains(status, "status:** historical design record") {
		t.Fatal("execution model does not identify itself as a historical design record")
	}
	if !strings.Contains(status, "not a description of current behavior") {
		t.Fatal("execution model does not disclaim current-behavior authority")
	}
}

func TestGeneratedPrefersDocumentationStatesStoredBehavior(t *testing.T) {
	for _, path := range []string{"../docs/sdk-reference.md", "../pkg/docs/mirror/sdk-reference.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, receiver := range []string{"JobNode", "JobGroup"} {
			needle := "*" + receiver + ") Prefers("
			for _, line := range strings.Split(string(data), "\n") {
				if strings.Contains(line, needle) {
					assertPreferenceContract(t, strings.ToLower(line))
					needle = ""
					break
				}
			}
			if needle != "" {
				t.Fatalf("%s has no generated %s.Prefers entry", path, receiver)
			}
		}
	}
}

func TestSchedulingPrefersDocumentationStatesStoredBehavior(t *testing.T) {
	for _, path := range []string{"../docs/scheduling.md", "../pkg/docs/mirror/scheduling.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		start := strings.Index(text, "- **`Prefers`**")
		if start < 0 {
			t.Fatalf("%s has no Prefers definition", path)
		}
		definition := text[start:]
		if end := strings.Index(definition, "\n- **`"); end >= 0 {
			definition = definition[:end]
		}
		assertPreferenceContract(t, strings.ToLower(definition))
	}
}

func methodDoc(t *testing.T, path, receiver, method string) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != method || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		name, ok := star.X.(*ast.Ident)
		if !ok || name.Name != receiver {
			continue
		}
		if fn.Doc == nil {
			t.Fatalf("%s.%s has no documentation", receiver, method)
		}
		return strings.ToLower(fn.Doc.Text())
	}
	t.Fatalf("%s.%s not found", receiver, method)
	return ""
}

func assertPreferenceContract(t *testing.T, doc string) {
	t.Helper()
	doc = strings.Join(strings.Fields(doc), " ")
	for _, want := range []string{"plan-snapshot metadata", "do not affect runner selection"} {
		if !strings.Contains(doc, want) {
			t.Errorf("preference documentation does not contain %q", want)
		}
	}
	for _, falseClaim := range []string{
		"profile default",
		"default runner",
		"biases runner selection",
		"dispatch snapshot",
		"pipeline explain",
		"renderer",
		"dashboard",
	} {
		if strings.Contains(doc, falseClaim) {
			t.Errorf("preference documentation contains false claim %q", falseClaim)
		}
	}
}
