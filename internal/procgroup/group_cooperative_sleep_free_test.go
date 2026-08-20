//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestCooperativeSessionCleanupDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse group_test.go: %v", err)
	}
	foundMode := false
	foundTest := false
	usesReadiness := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "TestTerminateSessionAllowsCooperativeCleanupBeforeEscalation":
			foundTest = true
			rejectTimeSleep(t, fset, fn.Name.Name, fn.Body)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForProcgroupReady" {
					usesReadiness = true
				}
				return true
			})
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
				if err != nil || mode != "session-cooperative" {
					return true
				}
				foundMode = true
				for _, stmt := range clause.Body {
					rejectTimeSleep(t, fset, mode, stmt)
				}
				return false
			})
		}
	}
	if !foundMode {
		t.Error("TestGroupHelperProcess does not declare session-cooperative mode")
	}
	if !foundTest {
		t.Error("group_test.go does not declare TestTerminateSessionAllowsCooperativeCleanupBeforeEscalation")
	}
	if !usesReadiness {
		t.Error("cooperative cleanup test does not use waitForProcgroupReady")
	}
}
