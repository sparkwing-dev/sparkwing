package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	rowLockClause   = regexp.MustCompile(`\bFOR\s+(NO\s+KEY\s+UPDATE|KEY\s+SHARE|UPDATE|SHARE)\b`)
	authorityRowSQL = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|UPDATE|INTO)\s+(?:teams|memberships|sessions|capabilities)\b`)
)

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
			if literal, ok := n.(*ast.CompositeLit); ok {
				if typ, ok := literal.Type.(*ast.Ident); ok && typ.Name == "ledgerTx" {
					t.Errorf("%s: obtain ledgerTx through lockLedgerTx", fset.Position(literal.Pos()))
				}
			}
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && (rowLockClause.MatchString(lit.Value) || strings.Contains(lit.Value, "sparkwing/ledger/")) {
				t.Errorf("%s: row lock outside lock_order.go: %s", fset.Position(lit.Pos()), lit.Value)
			}
			if expression, ok := n.(*ast.BinaryExpr); ok && locksAuthorityRows(expression) {
				t.Errorf("%s: declare authority rows through acquireLockSet", fset.Position(expression.Pos()))
			}
			return true
		})
	}
}

func locksAuthorityRows(expression ast.Expr) bool {
	var authority, lock bool
	ast.Inspect(expression, func(n ast.Node) bool {
		if literal, ok := n.(*ast.BasicLit); ok && literal.Kind == token.STRING {
			if value, err := strconv.Unquote(literal.Value); err == nil && authorityRowSQL.MatchString(value) {
				authority = true
			}
		}
		if call, ok := n.(*ast.CallExpr); ok {
			if method, ok := call.Fun.(*ast.SelectorExpr); ok {
				switch method.Sel.Name {
				case "forUpdate", "forNoKeyUpdate", "forShare", "forKeyShare":
					lock = true
				}
			}
		}
		return true
	})
	return authority && lock
}
