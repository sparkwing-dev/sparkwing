package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSpawnDispatchRegressionsDoNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "spawn_dispatch_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse spawn_dispatch_test.go: %v", err)
	}
	usesPausedCheck := false
	usesForcedExpiry := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if pkg.Name == "time" && sel.Sel.Name == "Sleep" {
				t.Errorf("%s uses time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			if fn.Name.Name == "TestSpawnDispatch_NoProgressTimeoutPausesForChildAndResumesAfterward" && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "ProgressTimeoutPausedForTest":
					usesPausedCheck = true
				case "ExpireProgressTimeoutForTest":
					usesForcedExpiry = true
				}
			}
			return true
		})
	}
	if !usesPausedCheck {
		t.Error("spawn progress regression does not inspect the paused timeout controller")
	}
	if !usesForcedExpiry {
		t.Error("spawn progress regression does not force timeout expiry")
	}
}
