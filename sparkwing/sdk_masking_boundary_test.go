package sparkwing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDKHasNoMaskerOrAutomaticRegistration(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range tree.Imports {
			if strings.Contains(imp.Path.Value, "/internal/secrets") && path != "secret.go" {
				t.Errorf("SDK source %s imports the host masker", path)
			}
		}
		ast.Inspect(tree, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.Ident:
				if n.Name == "planningDiagnostics" || n.Name == "Masker" {
					t.Errorf("SDK source %s retains %s", path, n.Name)
				}
			case *ast.SelectorExpr:
				if (n.Sel.Name == "Mask" || n.Sel.Name == "RegisterList") && path != "sdk_contract.go" {
					t.Errorf("SDK source %s registers a mask outside explicit Mask", path)
				}
			}
			return true
		})
	}
	tsRoot := filepath.Join("..", "sdk", "typescript", "src")
	if _, err := os.Stat(filepath.Join(tsRoot, "diagnostics.ts")); !os.IsNotExist(err) {
		t.Fatal("TypeScript SDK retains diagnostics.ts masker")
	}
	for _, name := range []string{"describe.ts", "plan.ts", "runner.ts"} {
		raw, err := os.ReadFile(filepath.Join(tsRoot, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"PlanningDiagnostics", "Masker", ".mask("} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("TypeScript %s retains %s", name, forbidden)
			}
		}
	}
}
