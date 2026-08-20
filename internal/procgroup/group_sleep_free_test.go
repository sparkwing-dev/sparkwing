//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCooperativeTerminationReadinessDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse group_test.go: %v", err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestTerminateSessionAllowsCooperativeCleanupBeforeEscalation" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" {
				t.Errorf("cooperative readiness contains time.Sleep at %s", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("group_test.go does not declare cooperative termination regression")
	}
}
