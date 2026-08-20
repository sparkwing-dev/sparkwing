package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestElectionExactlyOneWinnerOwnsTimers(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "integration_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse integration_test.go: %v", err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "TestElection_ExactlyOneWinner" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("integration_test.go does not declare TestElection_ExactlyOneWinner")
	}
	constructors := map[string]string{}
	stops := map[string]int{}
	ast.Inspect(target.Body, func(node ast.Node) bool {
		if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
			name, nameOK := assign.Lhs[0].(*ast.Ident)
			call, callOK := assign.Rhs[0].(*ast.CallExpr)
			if nameOK && callOK {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" &&
						(sel.Sel.Name == "NewTimer" || sel.Sel.Name == "NewTicker") {
						constructors[name.Name] = sel.Sel.Name
					}
				}
			}
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
				switch sel.Sel.Name {
				case "After":
					t.Errorf("TestElection_ExactlyOneWinner contains time.After at %s", fset.Position(call.Pos()))
				}
			}
			if sel.Sel.Name == "Stop" {
				if receiver, ok := sel.X.(*ast.Ident); ok {
					stops[receiver.Name]++
				}
			}
		}
		return true
	})
	wantConstructors := map[string]string{
		"deadline":     "NewTimer",
		"poll":         "NewTicker",
		"loseDeadline": "NewTimer",
	}
	for name, constructor := range wantConstructors {
		if constructors[name] != constructor {
			t.Errorf("%s constructor = time.%s, want time.%s", name, constructors[name], constructor)
		}
	}
	wantStops := map[string]int{"deadline": 1, "poll": 2, "loseDeadline": 1}
	for name, count := range wantStops {
		if stops[name] != count {
			t.Errorf("%s Stop calls = %d, want %d", name, stops[name], count)
		}
	}
}
