//go:build !windows

package procgroup

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
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
	keepsStressCardinality := false
	ast.Inspect(target.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.ValueSpec:
			if len(node.Names) == 1 && node.Names[0].Name == "count" && len(node.Values) == 1 {
				if value, ok := node.Values[0].(*ast.BasicLit); ok && value.Kind == token.INT && value.Value == strconv.Itoa(50) {
					keepsStressCardinality = true
				}
			}
		case *ast.RangeStmt:
			over, ok := node.X.(*ast.Ident)
			if !ok {
				return true
			}
			switch over.Name {
			case "count":
				if rangeCallsCleanup(node) {
					registersCleanup = true
				}
			case "groups":
				if rangeLaunchesFinish(node) {
					finishesInWorker = true
				}
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
	if !keepsStressCardinality {
		t.Error("lifecycle stress must retain 50 process groups")
	}
}

func rangeCallsCleanup(loop *ast.RangeStmt) bool {
	found := false
	ast.Inspect(loop.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, receiverOK := sel.X.(*ast.Ident)
		if receiverOK && receiver.Name == "t" && sel.Sel.Name == "Cleanup" {
			found = true
		}
		return true
	})
	return found
}

func rangeLaunchesFinish(loop *ast.RangeStmt) bool {
	for _, stmt := range loop.Body.List {
		worker, ok := stmt.(*ast.GoStmt)
		if !ok {
			continue
		}
		found := false
		ast.Inspect(worker.Call, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Finish" {
				found = true
			}
			return true
		})
		if found {
			return true
		}
	}
	return false
}
