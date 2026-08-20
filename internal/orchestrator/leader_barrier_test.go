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
	closesHolding := map[string]bool{"held": false, "heldSkip": false}
	receivesRelease := map[string]bool{"held": false, "heldSkip": false}
	waitReceivesHolding := false
	releaseClosesRelease := false
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
			if receive, ok := node.(*ast.UnaryExpr); ok && receive.Op == token.ARROW {
				if sel, ok := receive.X.(*ast.SelectorExpr); ok {
					if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == "barrier" {
						switch {
						case sel.Sel.Name == "release" && (fn.Name.Name == "held" || fn.Name.Name == "heldSkip"):
							receivesRelease[fn.Name.Name] = true
						case sel.Sel.Name == "holding" && fn.Name.Name == "waitForLeaderHolding":
							waitReceivesHolding = true
						}
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "close" && len(call.Args) == 1 {
				if sel, ok := call.Args[0].(*ast.SelectorExpr); ok {
					if barrier, ok := sel.X.(*ast.Ident); ok && barrier.Name == "barrier" {
						switch {
						case sel.Sel.Name == "holding" && (fn.Name.Name == "held" || fn.Name.Name == "heldSkip"):
							closesHolding[fn.Name.Name] = true
						case sel.Sel.Name == "release" && fn.Name.Name == "releaseLeaderBarrier":
							releaseClosesRelease = true
						}
					}
				}
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" &&
					(sel.Sel.Name == "Sleep" || sel.Sel.Name == "After" || sel.Sel.Name == "NewTicker" || sel.Sel.Name == "Now") {
					t.Errorf("%s contains time.%s polling at %s", fn.Name.Name, sel.Sel.Name, fset.Position(call.Pos()))
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
	for name := range closesHolding {
		if !closesHolding[name] || !receivesRelease[name] {
			t.Errorf("%s does not signal holding and receive release through the leader barrier", name)
		}
	}
	if !waitReceivesHolding {
		t.Error("waitForLeaderHolding does not receive the holding signal")
	}
	if !releaseClosesRelease {
		t.Error("releaseLeaderBarrier does not close the release signal")
	}
}
