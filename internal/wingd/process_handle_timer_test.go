package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestProcessHandleWaitsOwnTimers(t *testing.T) {
	targets := map[string]bool{
		"waitOK":         false,
		"waitLine":       false,
		"mustStayQueued": false,
	}
	constructs := map[string]int{}
	allTimers := map[string]int{}
	stops := map[string]bool{}
	receives := map[string]bool{}
	mustStayQueuedLoops := false
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "process_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse process_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if fn.Name.Name == "mustStayQueued" {
				if _, ok := node.(*ast.ForStmt); ok {
					mustStayQueuedLoops = true
				}
			}
			if receive, ok := node.(*ast.UnaryExpr); ok && receive.Op == token.ARROW {
				if channel, ok := receive.X.(*ast.SelectorExpr); ok && channel.Sel.Name == "C" {
					if timer, ok := channel.X.(*ast.Ident); ok && timer.Name == "timer" {
						receives[fn.Name.Name] = true
					}
				}
			}
			if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
				name, nameOK := assign.Lhs[0].(*ast.Ident)
				call, callOK := assign.Rhs[0].(*ast.CallExpr)
				if nameOK && name.Name == "timer" && callOK {
					if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "NewTimer" {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
							constructs[fn.Name.Name]++
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
						t.Errorf("%s contains time.After at %s", fn.Name.Name, fset.Position(call.Pos()))
					case "NewTimer":
						allTimers[fn.Name.Name]++
					case "NewTicker":
						t.Errorf("%s contains unexpected time.NewTicker at %s", fn.Name.Name, fset.Position(call.Pos()))
					}
				}
				if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "timer" && sel.Sel.Name == "Stop" {
					stops[fn.Name.Name] = true
				}
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("process_test.go does not declare procHandle.%s", name)
		}
		if constructs[name] != 1 || allTimers[name] != 1 || !receives[name] || !stops[name] {
			t.Errorf("procHandle.%s does not construct, receive, and stop one owned timer", name)
		}
	}
	if !mustStayQueuedLoops {
		t.Error("procHandle.mustStayQueued does not observe output until its timer expires")
	}
}
