package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSuccessorGraceRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]string{
		"TestChurn_HolderWatchReattachesAcrossKill": "churn_test.go",
		"TestCancel_ReattachedHolderIsCancellable":  "cancel_reattach_test.go",
		"observeReattachedHolderFor":                "churn_test.go",
	}
	found := make(map[string]bool, len(targets))
	for name, filename := range targets {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			found[name] = true
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
					t.Errorf("%s uses time.Sleep at %s", name, fset.Position(call.Pos()))
				}
				return true
			})
		}
	}
	for name := range targets {
		if !found[name] {
			t.Errorf("%s declaration not found", name)
		}
	}
}
