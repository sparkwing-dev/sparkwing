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
	var timers, tickers, stops int
	ast.Inspect(target.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
				switch sel.Sel.Name {
				case "After":
					t.Errorf("TestElection_ExactlyOneWinner contains time.After at %s", fset.Position(call.Pos()))
				case "NewTimer":
					timers++
				case "NewTicker":
					tickers++
				}
			}
			if sel.Sel.Name == "Stop" {
				stops++
			}
		}
		return true
	})
	if timers != 2 || tickers != 1 || stops != 3 {
		t.Errorf("election timer ownership = %d timers, %d tickers, %d stops; want 2, 1, 3", timers, tickers, stops)
	}
}
