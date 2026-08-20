package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDaemonHarnessOwnsBackgroundRunLifecycle(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "harness_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse harness_test.go: %v", err)
	}
	var (
		hasFinishedField bool
		foundStart       bool
		foundStopWait    bool
		workerCompletes  bool
		cleanupPos       token.Pos
		readySelectPos   token.Pos
		stopCalled       bool
		stopTimer        bool
		stopTimerStopped bool
		boundedJoin      bool
	)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if ok {
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name.Name != "testDaemon" {
					continue
				}
				structure, ok := typeSpec.Type.(*ast.StructType)
				if !ok {
					continue
				}
				for _, field := range structure.Fields.List {
					if len(field.Names) != 1 || field.Names[0].Name != "finished" {
						continue
					}
					channel, ok := field.Type.(*ast.ChanType)
					if !ok {
						continue
					}
					_, hasFinishedField = channel.Value.(*ast.StructType)
				}
			}
			continue
		}
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		isStart := fn.Name.Name == "startDaemon"
		isStopWait := fn.Recv != nil && fn.Name.Name == "stopAndWait"
		if !isStart && !isStopWait {
			continue
		}
		foundStart = foundStart || isStart
		foundStopWait = foundStopWait || isStopWait
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if isStart {
				if selection, ok := node.(*ast.SelectStmt); ok && !readySelectPos.IsValid() {
					readySelectPos = selection.Pos()
				}
				if worker, ok := node.(*ast.GoStmt); ok {
					body, ok := worker.Call.Fun.(*ast.FuncLit)
					if ok {
						ast.Inspect(body.Body, func(node ast.Node) bool {
							deferred, ok := node.(*ast.DeferStmt)
							if !ok {
								return true
							}
							ident, ok := deferred.Call.Fun.(*ast.Ident)
							if !ok || ident.Name != "close" || len(deferred.Call.Args) != 1 {
								return true
							}
							field, ok := deferred.Call.Args[0].(*ast.SelectorExpr)
							workerCompletes = ok && field.Sel.Name == "finished"
							return true
						})
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
			receiver, _ := sel.X.(*ast.Ident)
			if isStart && receiver != nil && receiver.Name == "t" && sel.Sel.Name == "Cleanup" {
				if len(call.Args) == 1 {
					cleanup, ok := call.Args[0].(*ast.FuncLit)
					if ok {
						ast.Inspect(cleanup.Body, func(node ast.Node) bool {
							cleanupCall, ok := node.(*ast.CallExpr)
							if !ok {
								return true
							}
							cleanupSel, ok := cleanupCall.Fun.(*ast.SelectorExpr)
							if ok && cleanupSel.Sel.Name == "stopAndWait" {
								cleanupPos = call.Pos()
							}
							return true
						})
					}
				}
			}
			if isStopWait && receiver != nil {
				switch {
				case receiver.Name == "td" && sel.Sel.Name == "stop":
					stopCalled = true
				case receiver.Name == "time" && sel.Sel.Name == "NewTimer":
					stopTimer = true
				case receiver.Name == "timer" && sel.Sel.Name == "Stop":
					stopTimerStopped = true
				}
			}
			return true
		})
		if isStopWait {
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				selection, ok := node.(*ast.SelectStmt)
				if !ok {
					return true
				}
				var waitsFinished, waitsTimer bool
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
					channel, ok := recv.X.(*ast.SelectorExpr)
					if !ok || channel.Sel.Name == "" {
						continue
					}
					recvIdent, _ := channel.X.(*ast.Ident)
					waitsFinished = waitsFinished || (recvIdent != nil && recvIdent.Name == "td" && channel.Sel.Name == "finished")
					waitsTimer = waitsTimer || (recvIdent != nil && recvIdent.Name == "timer" && channel.Sel.Name == "C")
				}
				boundedJoin = waitsFinished && waitsTimer
				return true
			})
		}
	}
	if !hasFinishedField || !foundStart || !foundStopWait || !workerCompletes {
		t.Error("daemon harness must publish worker completion through testDaemon.finished")
	}
	if !cleanupPos.IsValid() || !readySelectPos.IsValid() || cleanupPos >= readySelectPos {
		t.Error("startDaemon must register stopAndWait cleanup before readiness can fail")
	}
	if !stopCalled || !stopTimer || !stopTimerStopped || !boundedJoin {
		t.Error("stopAndWait must cancel the daemon and boundedly join its completed worker")
	}
}
