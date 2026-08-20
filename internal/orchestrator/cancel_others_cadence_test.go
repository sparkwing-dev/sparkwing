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
	want := map[string]string{
		"supersessionTicker": "supersessionPollInterval",
		"t":                  "slotHeartbeatInterval",
	}
	found := make(map[string]bool, len(want))
	constructors := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "startSlotHeartbeat" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
				return true
			}
			lhs, ok := assignment.Lhs[0].(*ast.Ident)
			if !ok {
				return true
			}
			outer, ok := assignment.Rhs[0].(*ast.CallExpr)
			if !ok || len(outer.Args) != 1 {
				return true
			}
			constructor, ok := outer.Fun.(*ast.SelectorExpr)
			if !ok || constructor.Sel.Name != "NewTicker" {
				return true
			}
			receiver, ok := constructor.X.(*ast.Ident)
			if !ok || receiver.Name != "time" {
				return true
			}
			constructors++
			inner, ok := outer.Args[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			name, ok := inner.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			if want[lhs.Name] != name.Name {
				return true
			}
			switch lhs.Name {
			case "supersessionTicker":
				found[lhs.Name] = len(inner.Args) == 0 && assignment.Tok == token.ASSIGN
			case "t":
				if len(inner.Args) == 1 {
					arg, argOK := inner.Args[0].(*ast.Ident)
					found[lhs.Name] = argOK && arg.Name == "onLimit" && assignment.Tok == token.DEFINE
				}
			}
			return true
		})
		break
	}
	if constructors != len(want) {
		t.Errorf("startSlotHeartbeat ticker constructors = %d, want exactly %d", constructors, len(want))
	}
	for variable, helper := range want {
		if !found[variable] {
			t.Errorf("startSlotHeartbeat must assign %s from %s", variable, helper)
		}
	}
}
