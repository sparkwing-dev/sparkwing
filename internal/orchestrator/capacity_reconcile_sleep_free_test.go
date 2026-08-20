//go:build unix

package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCapacityReconciliationRegressionDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "capacity_reconcile_unix_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse capacity_reconcile_unix_test.go: %v", err)
	}
	targets := map[string]bool{
		"TestRecordRunProfile_SDKBurnerPeakNotDoubled": false,
		"reconcileSink.Push":                           false,
		"waitForReconcileSampleAfter":                  false,
	}
	waitCalls := 0
	zeroBoundaryWaits := 0
	reportedBoundaryWaits := 0
	var reportedChildCPUPos token.Pos
	var manualSamplePos token.Pos
	var reportedAtAssignmentPos token.Pos
	var reportedBoundaryPos token.Pos
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
		if _, ok := targets[name]; !ok {
			continue
		}
		targets[name] = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if assignment, ok := node.(*ast.AssignStmt); ok && name == "TestRecordRunProfile_SDKBurnerPeakNotDoubled" {
				for _, lhs := range assignment.Lhs {
					if ident, ok := lhs.(*ast.Ident); ok && ident.Name == "reportedAt" {
						reportedAtAssignmentPos = assignment.Pos()
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if callee, ok := call.Fun.(*ast.Ident); ok && name == "TestRecordRunProfile_SDKBurnerPeakNotDoubled" {
				switch callee.Name {
				case "waitForReconcileSampleAfter":
					waitCalls++
					if len(call.Args) == 4 {
						switch boundary := call.Args[3].(type) {
						case *ast.CompositeLit:
							sel, ok := boundary.Type.(*ast.SelectorExpr)
							if !ok {
								break
							}
							pkg, pkgOK := sel.X.(*ast.Ident)
							if pkgOK && pkg.Name == "time" && sel.Sel.Name == "Time" && len(boundary.Elts) == 0 {
								zeroBoundaryWaits++
							}
						case *ast.Ident:
							if boundary.Name == "reportedAt" {
								reportedBoundaryWaits++
								reportedBoundaryPos = call.Pos()
							}
						}
					}
				}
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" && sel.Sel.Name == "Sleep" {
				t.Errorf("%s uses time.Sleep; synchronize on persisted samples", name)
			}
			if ok && name == "TestRecordRunProfile_SDKBurnerPeakNotDoubled" && pkg.Name == "nodemetrics" && sel.Sel.Name == "AddReportedChildCPU" {
				reportedChildCPUPos = call.Pos()
			}
			if ok && name == "TestRecordRunProfile_SDKBurnerPeakNotDoubled" && pkg.Name == "st" && sel.Sel.Name == "AddNodeMetricSample" {
				manualSamplePos = call.Pos()
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("required capacity reconciliation target %s not found", name)
		}
	}
	if waitCalls != 2 {
		t.Errorf("capacity reconciliation regression waits for %d persisted sample boundaries, want 2", waitCalls)
	}
	if zeroBoundaryWaits != 1 || reportedBoundaryWaits != 1 {
		t.Errorf("sample boundaries = zero:%d reported:%d, want one of each", zeroBoundaryWaits, reportedBoundaryWaits)
	}
	if reportedChildCPUPos == token.NoPos || manualSamplePos <= reportedChildCPUPos || reportedAtAssignmentPos <= manualSamplePos || reportedBoundaryPos <= reportedAtAssignmentPos {
		t.Error("reportedAt must be captured after child attribution and its manual sample, then used by the later sampler wait")
	}
}
