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
	waitPositions := make(map[string]token.Pos)
	observePositions := make(map[string]token.Pos)
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
				if ident, ok := call.Fun.(*ast.Ident); ok {
					switch ident.Name {
					case "waitForHolder":
						waitPositions[name] = call.Pos()
					case "observeReattachedHolderFor":
						if len(call.Args) == 4 {
							duration, durationOK := call.Args[3].(*ast.BinaryExpr)
							if durationOK && duration.Op == token.ADD {
								grace, graceOK := duration.X.(*ast.Ident)
								margin, marginOK := duration.Y.(*ast.BinaryExpr)
								if graceOK && grace.Name == "successorGrace" && marginOK && margin.Op == token.MUL {
									amount, amountOK := margin.X.(*ast.BasicLit)
									unit, unitOK := margin.Y.(*ast.SelectorExpr)
									if amountOK && amount.Value == "500" && unitOK && unit.Sel.Name == "Millisecond" {
										if pkg, ok := unit.X.(*ast.Ident); ok && pkg.Name == "time" {
											observePositions[name] = call.Pos()
										}
									}
								}
							}
						}
					}
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
	for _, name := range []string{"TestChurn_HolderWatchReattachesAcrossKill", "TestCancel_ReattachedHolderIsCancellable"} {
		waitPos := waitPositions[name]
		observePos := observePositions[name]
		if !waitPos.IsValid() || !observePos.IsValid() || waitPos >= observePos {
			t.Errorf("%s must find then observe the reattached holder through successorGrace + 500*time.Millisecond", name)
		}
	}
}
