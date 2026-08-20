package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCancelOthersRegressionsControlSupersessionCadence(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cache_dispatch_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse cache_dispatch_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "testCancelOthersStopsLeader" || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "SetSlotObservationIntervalForTest" {
				return true
			}
			receiver, ok := sel.X.(*ast.Ident)
			if !ok || receiver.Name != "orchestrator" {
				return true
			}
			binary, ok := call.Args[1].(*ast.BinaryExpr)
			if !ok || binary.Op != token.MUL {
				return true
			}
			count, countOK := binary.X.(*ast.BasicLit)
			unit, unitOK := binary.Y.(*ast.SelectorExpr)
			if countOK && count.Value == "10" && unitOK && unit.Sel.Name == "Millisecond" {
				found = true
			}
			return true
		})
		if found {
			return
		}
		break
	}
	t.Fatal("testCancelOthersStopsLeader must set the slot observation interval to 10ms")
}
