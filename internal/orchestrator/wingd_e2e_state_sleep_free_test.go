package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestWingdE2EStatePollingDoesNotSleep(t *testing.T) {
	targets := map[string]bool{
		"TestWingd_QueuedRunReemitsWaitStatusAndAnnouncesAdmission": false,
		"awaitEvent": false,
		"TestWingd_AbruptDeathReleasesLeaseFinalizesRunAndPromotesNext":    false,
		"TestWingd_DaemonFirstCancelRecoversStalledHolderWithoutDashboard": false,
		"TestWingd_DaemonFirstCancelSurvivesImmediateClientExit":           false,
		"TestWingd_DaemonFirstCancelFinalizesQueuedClient":                 false,
		"TestWingd_DaemonFirstCancelRemovesQueuedWaiterWithoutDashboard":   false,
		"TestRunLocal_SIGINTFinalizesRunAsCancelledAndReleasesLease":       false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wingd_e2e_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse wingd_e2e_test.go: %v", err)
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
