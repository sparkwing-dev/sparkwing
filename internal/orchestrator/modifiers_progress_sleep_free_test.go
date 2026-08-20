package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestObservableProgressResetRegressionDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "modifiers_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse modifiers_test.go: %v", err)
	}
	foundFixture := false
	foundRegression := false
	capturesGeneration := false
	testsStaleGeneration := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		name := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			if receiver, ok := fn.Recv.List[0].Type.(*ast.Ident); ok {
				name = receiver.Name + "." + name
			}
		}
		if name != "progressingPipe.Plan" && name != "TestNoProgressTimeout_ResetsOnObservableProgress" {
			continue
		}
		if name == "progressingPipe.Plan" {
			foundFixture = true
		} else {
			foundRegression = true
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
				t.Errorf("%s uses time.Sleep; synchronize on logged progress", name)
			}
			if name == "TestNoProgressTimeout_ResetsOnObservableProgress" && pkg.Name == "orchestrator" {
				switch sel.Sel.Name {
				case "ProgressTimeoutGenerationForTest":
					capturesGeneration = true
				case "ExpireProgressTimeoutGenerationForTest":
					testsStaleGeneration = true
				}
			}
			return true
		})
	}
	if !foundFixture {
		t.Error("required fixture progressingPipe.Plan not found")
	}
	if !foundRegression {
		t.Error("required progress-reset regression not found")
	}
	if !capturesGeneration || !testsStaleGeneration {
		t.Error("progress-reset regression must prove logged progress invalidates the prior timeout generation")
	}
}
