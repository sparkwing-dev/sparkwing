package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A Store method that hands its call to the default team serves the laptop
// store and the callers that predate the tenant key. Called from inside this
// package it answers about the default team alone, so a sweep, a claim path or
// a tenant method that reaches one acts on nothing in every other team, and
// nothing but a test with a second team notices. This keeps every such twin
// out of the package's own code.
func TestTenantTwins_NoStoreCodeCallsADefaultTeamTwin(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, f)
	}
	twins := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv != nil && exprString(fn.Recv.List[0].Type) == "*Store" && delegatesToDefaultTeam(fn) {
				twins[fn.Name.Name] = true
			}
		}
	}
	if len(twins) < 50 {
		t.Fatalf("found only %d default-team twins; the guard no longer reads this package", len(twins))
	}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || (fn.Recv != nil && exprString(fn.Recv.List[0].Type) == "*Store" && twins[fn.Name.Name]) {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !twins[sel.Sel.Name] {
					return true
				}
				if recv := exprString(sel.X); recv == "s" || recv == "t.s" || recv == "o.s" {
					t.Errorf("%s: %s calls %s.%s, which acts on the default team only; "+
						"call it through the team the row belongs to",
						fset.Position(call.Pos()), funcKey(fn), recv, sel.Sel.Name)
				}
				return true
			})
		}
	}
}

func delegatesToDefaultTeam(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	inner, ok := sel.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	innerSel, ok := inner.Fun.(*ast.SelectorExpr)
	return ok && innerSel.Sel.Name == "defaultTenant"
}
