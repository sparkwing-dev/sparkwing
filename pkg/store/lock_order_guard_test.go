package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var rowLockClause = regexp.MustCompile(`\bFOR\s+(NO\s+KEY\s+UPDATE|KEY\s+SHARE|UPDATE|SHARE)\b`)

// Row-lock clauses may be spelled only in lock_order.go. This checks where a
// clause is written, not the order a transaction takes its locks in.
func TestRowLockClausesLiveInLockOrder(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "lock_order.go" {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && rowLockClause.MatchString(lit.Value) {
				t.Errorf("%s: row lock outside lock_order.go: %s", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
	}
}
