package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestProcessSuccessorUsesOnlyTheLeaseObservationSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "process_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var foundHelper bool
	var observationSleeps int
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		switch fn.Name.Name {
		case "waitForDaemonLineCount":
			foundHelper = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if isTimeSleep(node) {
					t.Errorf("%s: poll successor readiness with timers", fset.Position(node.Pos()))
				}
				return true
			})
		case "TestProcess_DaemonKillRestoresAndReattaches":
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if isTimeSleep(node) {
					observationSleeps++
				}
				return true
			})
		}
	}
	if !foundHelper {
		t.Error("required declaration waitForDaemonLineCount not found")
	}
	if observationSleeps != 1 {
		t.Errorf("successor regression has %d time.Sleep calls, want the one lease-survival observation", observationSleeps)
	}
}

func isTimeSleep(node ast.Node) bool {
	call, ok := node.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Sleep" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}
