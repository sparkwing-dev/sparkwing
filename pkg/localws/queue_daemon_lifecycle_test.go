package localws

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestQueueDaemonHarnessOwnsWorkerLifecycle(t *testing.T) {
	t.Parallel()

	file, err := parser.ParseFile(token.NewFileSet(), "queue_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse queue_test.go: %v", err)
	}
	var (
		found           bool
		workerCompletes bool
		cleanupPos      token.Pos
		readySelectPos  token.Pos
		cleanupCancels  bool
		cleanupStops    bool
		boundedJoin     bool
	)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "startDaemonForQueue" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if selection, ok := node.(*ast.SelectStmt); ok && !readySelectPos.IsValid() {
				readySelectPos = selection.Pos()
			}
			if worker, ok := node.(*ast.GoStmt); ok {
				body, ok := worker.Call.Fun.(*ast.FuncLit)
				if ok && len(body.Body.List) > 0 {
					deferred, ok := body.Body.List[0].(*ast.DeferStmt)
					if ok {
						ident, ok := deferred.Call.Fun.(*ast.Ident)
						if ok && ident.Name == "close" && len(deferred.Call.Args) == 1 {
							done, ok := deferred.Call.Args[0].(*ast.Ident)
							workerCompletes = ok && done.Name == "finished"
						}
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
			if receiver == nil || receiver.Name != "t" || sel.Sel.Name != "Cleanup" || len(call.Args) != 1 {
				return true
			}
			cleanup, ok := call.Args[0].(*ast.FuncLit)
			if !ok {
				return true
			}
			cleanupPos = call.Pos()
			ast.Inspect(cleanup.Body, func(node ast.Node) bool {
				if selection, ok := node.(*ast.SelectStmt); ok {
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
						if ident, ok := recv.X.(*ast.Ident); ok && ident.Name == "finished" {
							waitsFinished = true
						}
						if channel, ok := recv.X.(*ast.SelectorExpr); ok && channel.Sel.Name == "C" {
							ident, ok := channel.X.(*ast.Ident)
							waitsTimer = ok && ident.Name == "timer"
						}
					}
					boundedJoin = waitsFinished && waitsTimer
				}
				innerCall, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				if ident, ok := innerCall.Fun.(*ast.Ident); ok && ident.Name == "cancel" && len(innerCall.Args) == 0 {
					cleanupCancels = true
					return true
				}
				innerSel, ok := innerCall.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				innerReceiver, _ := innerSel.X.(*ast.Ident)
				cleanupStops = cleanupStops || (innerReceiver != nil && innerReceiver.Name == "timer" && innerSel.Sel.Name == "Stop")
				return true
			})
			return true
		})
	}
	if !found || !workerCompletes {
		t.Error("queue daemon harness must publish completion from its worker exit")
	}
	if !cleanupPos.IsValid() || !readySelectPos.IsValid() || cleanupPos >= readySelectPos {
		t.Error("queue daemon cleanup must be registered before readiness can fail")
	}
	if !cleanupCancels || !cleanupStops || !boundedJoin {
		t.Error("queue daemon cleanup must cancel and boundedly join its worker")
	}
}
