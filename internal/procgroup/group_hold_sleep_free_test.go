//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestGroupHelperIndefiniteHoldsDoNotUseTimeSleep(t *testing.T) {
	modes := map[string]bool{
		"concurrent-cleanup": false,
		"descendant":         false,
		"session-stubborn":   false,
		"session-parked":     false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse group_test.go: %v", err)
	}
	foundHelper := false
	foundReadyReader := false
	foundConcurrentStarter := false
	foundConcurrentTest := false
	usesConcurrentStarter := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "holdHelperProcess":
			foundHelper = true
			rejectTimeSleep(t, fset, fn.Name.Name, fn.Body)
		case "awaitProcgroupReadyByte":
			foundReadyReader = true
			rejectTimeSleep(t, fset, fn.Name.Name, fn.Body)
		case "startConcurrentCleanupHelper":
			foundConcurrentStarter = true
			rejectTimeSleep(t, fset, fn.Name.Name, fn.Body)
		case "TestConcurrentFinishAndTerminateNeverLoseCompletedCleanup":
			foundConcurrentTest = true
			rejectTimeSleep(t, fset, fn.Name.Name, fn.Body)
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "startConcurrentCleanupHelper" {
					usesConcurrentStarter = true
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
				if err != nil {
					return true
				}
				if _, ok := modes[mode]; !ok {
					return true
				}
				modes[mode] = true
				usesHold := false
				for _, stmt := range clause.Body {
					ast.Inspect(stmt, func(node ast.Node) bool {
						call, ok := node.(*ast.CallExpr)
						if !ok {
							return true
						}
						if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "holdHelperProcess" {
							usesHold = true
						}
						rejectSleepCall(t, fset, mode, call)
						return true
					})
				}
				if !usesHold {
					t.Errorf("%s does not use holdHelperProcess", mode)
				}
				return false
			})
		}
	}
	if !foundHelper {
		t.Error("group_test.go does not declare holdHelperProcess")
	}
	if !foundReadyReader {
		t.Error("group_test.go does not declare awaitProcgroupReadyByte")
	}
	if !foundConcurrentStarter {
		t.Error("group_test.go does not declare startConcurrentCleanupHelper")
	}
	if !foundConcurrentTest {
		t.Error("group_test.go does not declare TestConcurrentFinishAndTerminateNeverLoseCompletedCleanup")
	}
	if !usesConcurrentStarter {
		t.Error("concurrent cleanup test does not use startConcurrentCleanupHelper")
	}
	for mode, found := range modes {
		if !found {
			t.Errorf("TestGroupHelperProcess does not declare %q mode", mode)
		}
	}
}

func rejectTimeSleep(t *testing.T, fset *token.FileSet, owner string, node ast.Node) {
	t.Helper()
	ast.Inspect(node, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if ok {
			rejectSleepCall(t, fset, owner, call)
		}
		return true
	})
}

func rejectSleepCall(t *testing.T, fset *token.FileSet, owner string, call *ast.CallExpr) {
	t.Helper()
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sleep" {
		return
	}
	if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
		t.Errorf("%s contains time.Sleep at %s", owner, fset.Position(call.Pos()))
	}
}
