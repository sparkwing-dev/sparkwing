//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestGroupLifecycleStressFinishesGroupsConcurrently(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "group_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "TestGroupLifecycleStressLeavesEveryGroupReaped" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("TestGroupLifecycleStressLeavesEveryGroupReaped declaration missing")
	}
	finishesInWorker := false
	registersCleanup := false
	ast.Inspect(target.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.GoStmt:
			ast.Inspect(node.Call, func(child ast.Node) bool {
				call, ok := child.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && sel.Sel.Name == "Finish" {
					finishesInWorker = true
				}
				return true
			})
		case *ast.CallExpr:
			sel, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Cleanup" {
				return true
			}
			receiver, ok := sel.X.(*ast.Ident)
			if ok && receiver.Name == "t" {
				registersCleanup = true
			}
		}
		return true
	})
	if !finishesInWorker {
		t.Error("lifecycle stress must finish independent groups concurrently")
	}
	if !registersCleanup {
		t.Error("lifecycle stress must register cleanup as each group starts")
	}
}
