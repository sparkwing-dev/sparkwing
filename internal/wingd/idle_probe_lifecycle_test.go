package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestIdleProbeLoopsOwnTheirLifecycle(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "idle_probe_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse idle_probe_test.go: %v", err)
	}
	callers := map[string]bool{
		"TestIdleExit_HealthProbeTrafficDoesNotResetIdleClock": false,
		"TestIdleExit_QueryTrafficDoesNotResetIdleClock":       false,
		"TestIdleExit_SocketSweepProbeDoesNotResetIdleClock":   false,
		"TestIdleExit_PreHelloConnectionsDoNotResetIdleClock":  false,
		"TestIdleExit_GraceThenIdleUnderHealthProbes":          false,
	}
	var (
		foundHelper       bool
		foundOldHelper    bool
		helperWithCancel  bool
		helperCleanup     bool
		helperCancels     bool
		helperClosesDone  bool
		helperWaitsDone   bool
		helperBoundedJoin bool
		helperOwnsTimer   bool
		helperStopsTimer  bool
		preHelloDialBound bool
	)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if fn.Name.Name == "probeLoop" {
			foundOldHelper = true
		}
		_, isCaller := callers[fn.Name.Name]
		isHelper := fn.Name.Name == "startProbeLoop"
		if isHelper {
			foundHelper = true
		}
		if !isCaller && !isHelper {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if worker, ok := node.(*ast.GoStmt); isHelper && ok {
				body, ok := worker.Call.Fun.(*ast.FuncLit)
				if ok && len(body.Body.List) > 0 {
					deferred, ok := body.Body.List[0].(*ast.DeferStmt)
					if ok {
						closeCall := deferred.Call
						closeIdent, ok := closeCall.Fun.(*ast.Ident)
						if ok && closeIdent.Name == "close" && len(closeCall.Args) == 1 {
							done, ok := closeCall.Args[0].(*ast.Ident)
							helperClosesDone = ok && done.Name == "done"
						}
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				if isCaller && ident.Name == "startProbeLoop" {
					callers[fn.Name.Name] = true
				}
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, _ := sel.X.(*ast.Ident)
			if isHelper && receiver != nil {
				switch {
				case receiver.Name == "context" && sel.Sel.Name == "WithCancel":
					helperWithCancel = true
				case receiver.Name == "t" && sel.Sel.Name == "Cleanup":
					helperCleanup = true
					if len(call.Args) == 1 {
						cleanup, ok := call.Args[0].(*ast.FuncLit)
						if ok {
							ast.Inspect(cleanup.Body, func(node ast.Node) bool {
								if selection, ok := node.(*ast.SelectStmt); ok {
									var waitsDone, waitsTimer bool
									for _, stmt := range selection.Body.List {
										clause, ok := stmt.(*ast.CommClause)
										if !ok {
											continue
										}
										expr, ok := clause.Comm.(*ast.ExprStmt)
										if !ok {
											continue
										}
										recv, ok := expr.X.(*ast.UnaryExpr)
										if !ok || recv.Op != token.ARROW {
											continue
										}
										if ident, ok := recv.X.(*ast.Ident); ok && ident.Name == "done" {
											waitsDone = true
										}
										if channel, ok := recv.X.(*ast.SelectorExpr); ok && channel.Sel.Name == "C" {
											receiver, ok := channel.X.(*ast.Ident)
											waitsTimer = ok && receiver.Name == "timer"
										}
									}
									helperBoundedJoin = waitsDone && waitsTimer
								}
								if unary, ok := node.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
									if ident, ok := unary.X.(*ast.Ident); ok && ident.Name == "done" {
										helperWaitsDone = true
									}
								}
								innerCall, ok := node.(*ast.CallExpr)
								if !ok {
									return true
								}
								if ident, ok := innerCall.Fun.(*ast.Ident); ok && ident.Name == "cancel" && len(innerCall.Args) == 0 {
									helperCancels = true
									return true
								}
								innerSel, ok := innerCall.Fun.(*ast.SelectorExpr)
								if !ok {
									return true
								}
								innerReceiver, _ := innerSel.X.(*ast.Ident)
								if innerReceiver != nil && innerReceiver.Name == "time" && innerSel.Sel.Name == "NewTimer" {
									helperOwnsTimer = true
								}
								if innerReceiver != nil && innerReceiver.Name == "timer" && innerSel.Sel.Name == "Stop" {
									helperStopsTimer = true
								}
								return true
							})
						}
					}
				}
			}
			if fn.Name.Name == "TestIdleExit_PreHelloConnectionsDoNotResetIdleClock" && sel.Sel.Name == "DialContext" {
				preHelloDialBound = true
			}
			return true
		})
	}
	if foundOldHelper {
		t.Error("probeLoop leaves lifecycle ownership at callers; use startProbeLoop")
	}
	if !foundHelper || !helperWithCancel || !helperCleanup || !helperCancels || !helperClosesDone || !helperWaitsDone || !helperBoundedJoin || !helperOwnsTimer || !helperStopsTimer {
		t.Error("startProbeLoop must own cancellation, completion, cleanup, and a stopped bounded join timer")
	}
	for name, delegates := range callers {
		if !delegates {
			t.Errorf("%s must delegate probe lifecycle to startProbeLoop", name)
		}
	}
	if !preHelloDialBound {
		t.Error("pre-hello probes must use context-bounded dialing")
	}
}
