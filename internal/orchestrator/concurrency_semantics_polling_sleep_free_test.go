package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestConcurrencySemanticsStatePollingDoesNotSleep(t *testing.T) {
	targets := map[string]bool{
		"waitForConcurrencyPoll": false,
		"waitForLeaderHolding":   false,
		"waitForCoalesceWaiter":  false,
		"waitForQueuedRun":       false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "concurrency_semantics_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse concurrency_semantics_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		name := fn.Name.Name
		if _, ok := targets[name]; !ok {
			continue
		}
		targets[name] = true
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
				t.Errorf("%s contains time.Sleep at %s", name, fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s not found", name)
		}
	}
}
