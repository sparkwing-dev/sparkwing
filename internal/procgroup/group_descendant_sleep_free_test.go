//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestGroupDescendantReadinessDoesNotUseTimeSleep(t *testing.T) {
	modes := map[string]bool{"leader": false, "session-leader": false}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse group_test.go: %v", err)
	}
	foundHelper := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "startReadyGroupDescendant":
			foundHelper = true
			rejectTimeSleep(t, fset, fn.Name.Name, fn.Body)
		case "TestGroupHelperProcess":
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				clause, ok := node.(*ast.CaseClause)
				if !ok || len(clause.List) != 1 {
					return true
				}
				lit, ok := clause.List[0].(*ast.BasicLit)
				if !ok {
					return true
				}
				mode, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				if _, ok := modes[mode]; !ok {
					return true
				}
				modes[mode] = true
				usesReadiness := false
				for _, stmt := range clause.Body {
					ast.Inspect(stmt, func(node ast.Node) bool {
						call, ok := node.(*ast.CallExpr)
						if !ok {
							return true
						}
						if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "startReadyGroupDescendant" {
							usesReadiness = true
						}
						rejectSleepCall(t, fset, mode, call)
						return true
					})
				}
				if !usesReadiness {
					t.Errorf("%s does not use startReadyGroupDescendant", mode)
				}
				return false
			})
		}
	}
	if !foundHelper {
		t.Error("group_test.go does not declare startReadyGroupDescendant")
	}
	for mode, found := range modes {
		if !found {
			t.Errorf("TestGroupHelperProcess does not declare %q mode", mode)
		}
	}
}
