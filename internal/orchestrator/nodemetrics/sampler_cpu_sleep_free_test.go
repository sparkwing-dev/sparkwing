package nodemetrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCPUAccountingRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"TestRun_CountsRawExecChildrenCPU":          false,
		"hasSampleAfter":                            false,
		"waitForSampleAfter":                        false,
		"TestCPUAccountingBurnerProcess":            false,
		"burnAndReap":                               false,
		"TestReadCPUTime_SubtractsReportedChildCPU": false,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sampler_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse sampler_test.go: %v", err)
	}
	rawExecSignalsSamples := false
	rawExecWaitsForSample := false
	rawExecBoundsSampler := false
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
			if fn.Name.Name == "TestRun_CountsRawExecChildrenCPU" {
				if field, ok := node.(*ast.KeyValueExpr); ok {
					if key, ok := field.Key.(*ast.Ident); ok && key.Name == "sampleReady" {
						rawExecSignalsSamples = true
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if fn.Name.Name == "TestRun_CountsRawExecChildrenCPU" && sel.Sel.Name == "waitForSampleAfter" {
				rawExecWaitsForSample = true
			}
			if fn.Name.Name == "TestRun_CountsRawExecChildrenCPU" && sel.Sel.Name == "WithTimeout" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "context" {
					rawExecBoundsSampler = true
				}
			}
			if sel.Sel.Name != "Sleep" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" {
				t.Errorf("%s uses time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s declaration not found", name)
		}
	}
	if !rawExecSignalsSamples || !rawExecWaitsForSample || !rawExecBoundsSampler {
		t.Error("TestRun_CountsRawExecChildrenCPU must configure and boundedly wait for sample publication")
	}
}
