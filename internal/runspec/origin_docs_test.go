package runspec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestSDKTriggerSourceNamesOnlyRealOrigins(t *testing.T) {
	origins := originValues(t)
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join("..", "..", "sparkwing", "context.go"), nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	var comment string
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.TypeSpec)
		if !ok || spec.Name.Name != "TriggerInfo" {
			return true
		}
		for _, field := range spec.Type.(*ast.StructType).Fields.List {
			if len(field.Names) == 1 && field.Names[0].Name == "Source" {
				comment = field.Doc.Text() + field.Comment.Text()
			}
		}
		return false
	})
	named := regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(comment, -1)
	if len(named) == 0 {
		t.Fatalf("TriggerInfo.Source documents no values: %q", comment)
	}
	for _, m := range named {
		if !origins[m[1]] {
			t.Errorf("TriggerInfo.Source documents %q, which no run carries; origins are %s", m[1], strings.Join(sortedKeys(origins), ", "))
		}
	}
	if !strings.Contains(comment, `"local"`) {
		t.Error(`TriggerInfo.Source does not document "local", the source of every sparkwing run`)
	}
}

func originValues(t *testing.T) map[string]bool {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "contracts.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	origins := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		spec, ok := node.(*ast.ValueSpec)
		if !ok {
			return true
		}
		if typ, ok := spec.Type.(*ast.Ident); ok && typ.Name == "Origin" {
			for _, value := range spec.Values {
				if literal, ok := value.(*ast.BasicLit); ok {
					origins[strings.Trim(literal.Value, `"`)] = true
				}
			}
		}
		return true
	})
	if !origins[string(OriginLocal)] {
		t.Fatalf("parsed origins %v miss %q", origins, OriginLocal)
	}
	return origins
}

func sortedKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
