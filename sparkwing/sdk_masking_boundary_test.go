package sparkwing

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSDKHasNoMaskerOrAutomaticRegistration(t *testing.T) {
	var files []string
	if err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && !strings.Contains(path, "testdata") && !strings.HasPrefix(path, "sparkwingtest"+string(filepath.Separator)) {
			files = append(files, path)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
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
	scanned := 0
	if err := filepath.WalkDir(tsRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".ts") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		scanned++
		for _, forbidden := range []string{"PlanningDiagnostics", "Masker", ".mask("} {
			if forbidden == ".mask(" && filepath.Base(path) == "context.ts" {
				continue
			}
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("TypeScript %s retains %s", path, forbidden)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if scanned == 0 {
		t.Fatal("found no TypeScript SDK sources to scan")
	}
}
