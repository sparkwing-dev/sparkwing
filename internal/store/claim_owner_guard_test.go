package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaimOwnerHasNoLauncherScanOrAwardBypass(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve claim module source")
	}
	directory := filepath.Dir(source)
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"launcherSweep", "launcher_sweep/", "launcher_claim_team", "queuedLauncherUnitTx", "SAVEPOINT launcher_award"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("%s restores %s", path, forbidden)
			}
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, "unit_claim.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claimUnit", "claimUnitTx"} {
		checked := false
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if ok && ident.Name == "queuedUnitTx" {
					checked = true
				}
				return true
			})
		}
		if !checked {
			t.Fatalf("%s bypasses SQL eligibility", name)
		}
	}
}
