//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestStubbornSessionReadinessDoesNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"TestSessionTerminateKillsStubbornLeaderAndNestedGroup": false,
		"waitForProcgroupReady":                                 false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse group_test.go: %v", err)
	}
	usesReadiness := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForProcgroupReady" {
				usesReadiness = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
				t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
	if !usesReadiness {
		t.Error("stubborn session test does not use waitForProcgroupReady")
	}
	for name, found := range targets {
		if !found {
			t.Errorf("group_test.go does not declare %s", name)
		}
	}
}
