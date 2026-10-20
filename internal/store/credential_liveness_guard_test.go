package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCapabilityLivenessSQLHasOneOwner(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") || name == "credential_liveness.go" {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			// Revoking expired records and scheduling the first successor are
			// mutations of ended authority, rather than live credential reads.
			if fn.Name.Name == "revokeSelectedCapabilitiesTx" || fn.Name.Name == "scheduleRotationTx" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				literal, ok := n.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err != nil {
					return true
				}
				value = strings.ToLower(value)
				if strings.Contains(value, "capabilities") && (strings.Contains(value, "revoked_at is null") || strings.Contains(value, "(expires_at is null") || strings.Contains(value, ".expires_at is null")) {
					t.Errorf("%s: capability liveness must use liveCredentialSQL", fset.Position(literal.Pos()))
				}
				return true
			})
		}
	}
}
