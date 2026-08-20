//go:build !windows

package procgroup

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestGroupLifecycleStressFinishesGroupsConcurrently(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "group_test.go", nil, 0)
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
				if rangeOwnsGroupCleanup(fset, node) {
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

func rangeOwnsGroupCleanup(fset *token.FileSet, loop *ast.RangeStmt) bool {
	const expected = `t.Cleanup(func() {
	if !group.Reaped() {
		terminateForTest(group)
	}
})`
	for _, stmt := range loop.Body.List {
		expr, ok := stmt.(*ast.ExprStmt)
		if !ok {
			continue
		}
		var rendered bytes.Buffer
		if err := format.Node(&rendered, fset, expr.X); err == nil && rendered.String() == expected {
			return true
		}
	}
	return false
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
