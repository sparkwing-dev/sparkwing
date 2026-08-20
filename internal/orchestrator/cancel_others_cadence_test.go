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

func TestSlotHeartbeatUsesControlledObservationCadences(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "concurrency_dispatch.go", nil, 0)
	if err != nil {
		t.Fatalf("parse concurrency_dispatch.go: %v", err)
	}
	want := map[string]bool{
		"supersessionPollInterval": false,
		"slotHeartbeatInterval":    false,
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "startSlotHeartbeat" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			outer, ok := node.(*ast.CallExpr)
			if !ok || len(outer.Args) != 1 {
				return true
			}
			constructor, ok := outer.Fun.(*ast.SelectorExpr)
			if !ok || constructor.Sel.Name != "NewTicker" {
				return true
			}
			inner, ok := outer.Args[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := inner.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			switch name.Name {
			case "supersessionPollInterval":
				want[name.Name] = len(inner.Args) == 0
			case "slotHeartbeatInterval":
				if len(inner.Args) == 1 {
					arg, argOK := inner.Args[0].(*ast.Ident)
					want[name.Name] = argOK && arg.Name == "onLimit"
				}
			}
			return true
		})
		break
	}
	for helper, found := range want {
		if !found {
			t.Errorf("startSlotHeartbeat must construct a ticker from %s", helper)
		}
	}
}
