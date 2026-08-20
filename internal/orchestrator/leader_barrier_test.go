package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestLeaderBarrierUsesSignalsInsteadOfPolling(t *testing.T) {
	targets := map[string]bool{
		"resetLeaderBarrier":   false,
		"releaseLeaderBarrier": false,
		"held":                 false,
		"heldSkip":             false,
		"waitForLeaderHolding": false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "concurrency_semantics_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse concurrency_semantics_test.go: %v", err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
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
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "After" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
					t.Errorf("%s contains time.After at %s", fn.Name.Name, fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("concurrency_semantics_test.go does not declare %s", name)
		}
	}
}
