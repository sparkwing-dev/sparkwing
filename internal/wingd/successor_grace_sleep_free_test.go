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
	helperDeadlineFromStart := false
	helperChecksElapsed := false
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
				if name == "observeReattachedHolderFor" {
					if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
						lhs, lhsOK := assign.Lhs[0].(*ast.Ident)
						call, callOK := assign.Rhs[0].(*ast.CallExpr)
						if lhsOK && lhs.Name == "deadlineAt" && callOK && len(call.Args) == 1 {
							if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Add" {
								receiver, receiverOK := sel.X.(*ast.Ident)
								arg, argOK := call.Args[0].(*ast.Ident)
								helperDeadlineFromStart = receiverOK && receiver.Name == "started" && argOK && arg.Name == "duration"
							}
						}
					}
					if ifStmt, ok := node.(*ast.IfStmt); ok {
						comparison, comparisonOK := ifStmt.Cond.(*ast.BinaryExpr)
						init, initOK := ifStmt.Init.(*ast.AssignStmt)
						if comparisonOK && comparison.Op == token.LSS && initOK && len(init.Lhs) == 1 && len(init.Rhs) == 1 {
							left, leftOK := comparison.X.(*ast.Ident)
							right, rightOK := comparison.Y.(*ast.Ident)
							elapsed, elapsedOK := init.Lhs[0].(*ast.Ident)
							since, sinceOK := init.Rhs[0].(*ast.CallExpr)
							if leftOK && left.Name == "elapsed" && rightOK && right.Name == "duration" && elapsedOK && elapsed.Name == "elapsed" && sinceOK && len(since.Args) == 1 {
								if sel, ok := since.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Since" {
									pkg, pkgOK := sel.X.(*ast.Ident)
									arg, argOK := since.Args[0].(*ast.Ident)
									helperChecksElapsed = pkgOK && pkg.Name == "time" && argOK && arg.Name == "started"
								}
							}
						}
					}
				}
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
	if !helperDeadlineFromStart || !helperChecksElapsed {
		t.Error("observeReattachedHolderFor must derive and verify the full observation duration")
	}
}
