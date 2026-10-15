package controller

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// safety: migrated handlers consume authorization; a second check can disagree with route policy.
func TestAuthorizedHandlersDoNotAuthorize(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		source, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, declaration := range source.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || !takesAuthorization(fn) {
				continue
			}
			checked++
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch callee := call.Fun.(type) {
				case *ast.Ident:
					name = callee.Name
				case *ast.SelectorExpr:
					name = callee.Sel.Name
				}
				if handlerAuthorizationCall(name) {
					t.Errorf("%s: %s performs %s after policy authorization", fset.Position(call.Pos()), fn.Name.Name, name)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no authorized handlers were checked")
	}
}

func takesAuthorization(fn *ast.FuncDecl) bool {
	for _, field := range fn.Type.Params.List {
		if sel, ok := field.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Authorized" {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "capability" {
				return true
			}
		}
	}
	return false
}

func handlerAuthorizationCall(name string) bool {
	if strings.HasPrefix(strings.ToLower(name), "authorize") || strings.HasPrefix(strings.ToLower(name), "authenticate") {
		return true
	}
	switch name {
	case "PrincipalFromContext", "accountPrincipal", "teamMember", "requireScope", "requireServiceScope", "requireOperator", "triggerClaimRequest", "requestTenant", "requestTeam":
		return true
	}
	return false
}
