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
	constructs := map[string]bool{}
	stops := map[string]bool{}
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
						constructs[fn.Name.Name] = true
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
		if !constructs[name] || !stops[name] {
			t.Errorf("procHandle.%s does not own and stop its timer", name)
		}
	}
}
