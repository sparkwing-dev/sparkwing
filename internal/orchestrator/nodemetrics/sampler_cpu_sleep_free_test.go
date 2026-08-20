package nodemetrics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestCPUAccountingRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"Push":                                      false,
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
	var burnPos, reapedAtPos, waitPos token.Pos
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
						makeCall, ok := field.Value.(*ast.CallExpr)
						if ok && len(makeCall.Args) == 2 {
							makeIdent, makeOK := makeCall.Fun.(*ast.Ident)
							capacity, capacityOK := makeCall.Args[1].(*ast.BasicLit)
							channel, channelOK := makeCall.Args[0].(*ast.ChanType)
							if makeOK && makeIdent.Name == "make" && capacityOK && capacity.Value == "1" && channelOK {
								if element, ok := channel.Value.(*ast.StructType); ok && element.Fields.NumFields() == 0 {
									rawExecSignalsSamples = true
								}
							}
						}
					}
				}
				if assign, ok := node.(*ast.AssignStmt); ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
					lhs, lhsOK := assign.Lhs[0].(*ast.Ident)
					call, callOK := assign.Rhs[0].(*ast.CallExpr)
					if lhsOK && lhs.Name == "reapedAt" && callOK {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Now" {
							if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
								reapedAtPos = assign.Pos()
							}
						}
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if fn.Name.Name == "TestRun_CountsRawExecChildrenCPU" {
				if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "burnAndReap" {
					burnPos = call.Pos()
				}
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if fn.Name.Name == "TestRun_CountsRawExecChildrenCPU" {
				if sel.Sel.Name == "waitForSampleAfter" && len(call.Args) == 2 {
					receiver, receiverOK := sel.X.(*ast.Ident)
					ctxArg, ctxOK := call.Args[0].(*ast.Ident)
					boundaryArg, boundaryOK := call.Args[1].(*ast.Ident)
					if receiverOK && receiver.Name == "sink" && ctxOK && ctxArg.Name == "ctx" && boundaryOK && boundaryArg.Name == "reapedAt" {
						rawExecWaitsForSample = true
						waitPos = call.Pos()
					}
				}
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
	orderedBoundary := burnPos.IsValid() && reapedAtPos.IsValid() && waitPos.IsValid() && burnPos < reapedAtPos && reapedAtPos < waitPos
	if !rawExecSignalsSamples || !rawExecWaitsForSample || !rawExecBoundsSampler || !orderedBoundary {
		t.Error("TestRun_CountsRawExecChildrenCPU must configure and boundedly wait for sample publication")
	}
}
