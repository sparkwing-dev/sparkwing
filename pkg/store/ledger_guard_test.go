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

var ledgerDML = regexp.MustCompile(`(?is)\b(insert\s+into|update|delete\s+from)\s+(credit_grants|credit_charges)\b`)

// The credit ledger is written only by ledgerTx in ledger.go, which a team's ledger lock is the only way to obtain,
// so a second writer of credit_grants or credit_charges, which could skip the lock, fails here.
func TestLedgerTablesAreWrittenOnlyThroughTheLedger(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: %v", fset.Position(lit.Pos()), err)
			}
			if m := ledgerDML.FindStringSubmatch(text); m != nil {
				checked++
				if path != "ledger.go" {
					t.Errorf("%s writes %s outside ledger.go; write it through ledgerTx", fset.Position(lit.Pos()), m[2])
				}
			}
			return true
		})
	}
	if checked == 0 {
		t.Fatal("found no ledger writes at all; the guard is not looking at the store")
	}
}
