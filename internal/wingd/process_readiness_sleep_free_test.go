package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestProcessFileReadinessDoesNotSleep(t *testing.T) {
	targets := map[string]bool{
		"readDaemonPid":                              false,
		"waitForNonemptyFile":                        false,
		"TestProcess_SelfSpawnedDaemonWritesLogFile": false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "process_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, targeted := targets[fn.Name.Name]; !targeted {
			continue
		}
		targets[fn.Name.Name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" {
				t.Errorf("%s: synchronize through file readiness", fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("required declaration %s not found", name)
		}
	}
}
