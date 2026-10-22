package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// Architectural lint restricts the known ledger writers. Production admission
// tests establish permit enforcement and complete-set behavior.
func TestAdmissionArchitectureHasOneGrantOwner(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Dir(file)
	reservationInsert := regexp.MustCompile(`(?i)\binsert\s+into\s+concurrency_(holders|waiters)\b`)
	permitGrant := regexp.MustCompile(`(?is)\binsert\s+into\s+admission_work\b|\bupdate\s+admission_work\s+set[^;]*\bgranted\s*=\s*1\b`)
	for _, dir := range []string{root, filepath.Join(root, "..", "nodehost")} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			tree, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			function := ""
			ast.Inspect(tree, func(node ast.Node) bool {
				if declaration, ok := node.(*ast.FuncDecl); ok {
					function = declaration.Name.Name
				}
				if literal, ok := node.(*ast.BasicLit); ok && literal.Kind == token.STRING {
					sql, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if match := reservationInsert.FindStringSubmatch(sql); match != nil {
						writer := "txPark"
						if strings.EqualFold(match[1], "holders") {
							writer = "txInsertHolder"
						}
						if dir != root || name != "concurrency.go" || function != writer {
							t.Errorf("%s writes reservations outside the ledger writer %s", filepath.Join(dir, name), writer)
						}
					}
					if permitGrant.MatchString(sql) && (dir != root || name != "capacity_admission.go") {
						t.Errorf("%s grants permits outside the admission owner", filepath.Join(dir, name))
					}
				}
				if call, ok := node.(*ast.CallExpr); ok {
					if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "txInsertHolder" && (dir != root || name != "concurrency.go" || function != "acquireConcurrencySlotTx") {
						t.Errorf("%s grants a holder outside the admission policy engine", filepath.Join(dir, name))
					}
					if selector, ok := call.Fun.(*ast.SelectorExpr); ok && selector.Sel.Name == "acquireConcurrencySlotTx" && (dir != root || name != "capacity_admission.go") {
						t.Errorf("%s creates logical capacity outside the admission owner", filepath.Join(dir, name))
					}
				}
				if declaration, ok := node.(*ast.TypeSpec); ok && (declaration.Name.Name == "NativeAdmission" || declaration.Name.Name == "storeAdmission") {
					t.Errorf("%s restores a host-side admission owner", filepath.Join(dir, name))
				}
				return true
			})
		}
	}
}
