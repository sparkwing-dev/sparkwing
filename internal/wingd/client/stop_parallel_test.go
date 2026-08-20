package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestStopReleaseRoundsRunAsParallelSubtests(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "stop_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundTarget, foundParallelRounds, roundConstants := false, false, 0
	for _, decl := range file.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
			for _, spec := range gen.Specs {
				value, ok := spec.(*ast.ValueSpec)
				if !ok || len(value.Names) != 1 || value.Names[0].Name != "stopReleaseRounds" {
					continue
				}
				roundConstants++
				if len(value.Values) != 1 || !isIntLiteral(value.Values[0], "20") {
					t.Error("stopReleaseRounds must equal 20")
				}
			}
		}
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestStop_ReturnsOnlyAfterTheDaemonReleasedTheHome" || fn.Body == nil {
			continue
		}
		foundTarget = true
		if len(fn.Body.List) != 1 {
			continue
		}
		loop, ok := fn.Body.List[0].(*ast.RangeStmt)
		if !ok || !isStopReleaseRoundsLoop(loop) || len(loop.Body.List) != 1 {
			continue
		}
		expr, ok := loop.Body.List[0].(*ast.ExprStmt)
		if !ok {
			continue
		}
		run, ok := expr.X.(*ast.CallExpr)
		if !ok || len(run.Args) != 2 {
			continue
		}
		sel, ok := run.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Run" {
			continue
		}
		receiver, ok := sel.X.(*ast.Ident)
		callback, okCallback := run.Args[1].(*ast.FuncLit)
		if !ok || receiver.Name != "t" || !okCallback || len(callback.Body.List) == 0 {
			continue
		}
		first, ok := callback.Body.List[0].(*ast.ExprStmt)
		if !ok {
			continue
		}
		parallel, ok := first.X.(*ast.CallExpr)
		if !ok || len(parallel.Args) != 0 {
			continue
		}
		parallelSel, ok := parallel.Fun.(*ast.SelectorExpr)
		if !ok || parallelSel.Sel.Name != "Parallel" {
			continue
		}
		parallelReceiver, receiverOK := parallelSel.X.(*ast.Ident)
		foundParallelRounds = receiverOK && parallelReceiver.Name == "t"
	}
	if !foundTarget {
		t.Fatal("stop_test.go does not declare TestStop_ReturnsOnlyAfterTheDaemonReleasedTheHome")
	}
	if !foundParallelRounds {
		t.Fatal("stop release rounds must each run as a direct parallel subtest")
	}
	if roundConstants != 1 {
		t.Fatalf("found %d stopReleaseRounds declarations, want exactly 1", roundConstants)
	}
}

func TestRunDaemonCleanupCancelsAndJoinsWorker(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "stop_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundTarget, workerClosesFinished, cleanupOwnsJoin := false, false, false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runDaemon" || fn.Body == nil {
			continue
		}
		foundTarget = true
		for _, stmt := range fn.Body.List {
			if goStmt, ok := stmt.(*ast.GoStmt); ok {
				worker, ok := goStmt.Call.Fun.(*ast.FuncLit)
				workerClosesFinished = ok && len(worker.Body.List) > 0 && isDeferClose(worker.Body.List[0], "finished")
			}
			expr, ok := stmt.(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expr.X.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			cleanup, cleanupOK := call.Args[0].(*ast.FuncLit)
			if !ok || sel.Sel.Name != "Cleanup" || !cleanupOK {
				continue
			}
			cleanupOwnsJoin = cleanupCancelsAndJoins(cleanup.Body)
		}
	}
	if !foundTarget {
		t.Fatal("stop_test.go does not declare runDaemon")
	}
	if !workerClosesFinished {
		t.Error("runDaemon worker must defer close(finished) as its first statement")
	}
	if !cleanupOwnsJoin {
		t.Error("runDaemon cleanup must cancel and select between finished and its owned timer")
	}
}

func isStopReleaseRoundsLoop(loop *ast.RangeStmt) bool {
	round, ok := loop.Key.(*ast.Ident)
	if !ok || round.Name != "round" || loop.Value != nil || loop.Tok != token.DEFINE {
		return false
	}
	rounds, ok := loop.X.(*ast.Ident)
	return ok && rounds.Name == "stopReleaseRounds"
}

func cleanupCancelsAndJoins(body *ast.BlockStmt) bool {
	return len(body.List) == 4 &&
		isDirectCall(body.List[0], "cancel") &&
		isCleanupTimer(body.List[1]) &&
		isDeferStop(body.List[2], "timer") &&
		selectWaitsForFinishedOrTimer(body.List[3])
}

func isDirectCall(stmt ast.Stmt, name string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	return ok && len(call.Args) == 0 && isIdent(call.Fun, name)
}

func isCleanupTimer(stmt ast.Stmt) bool {
	assign, ok := stmt.(*ast.AssignStmt)
	if !ok || assign.Tok != token.DEFINE || len(assign.Lhs) != 1 || !isIdent(assign.Lhs[0], "timer") || len(assign.Rhs) != 1 {
		return false
	}
	call, ok := assign.Rhs[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	constructor, ok := call.Fun.(*ast.SelectorExpr)
	duration, durationOK := call.Args[0].(*ast.SelectorExpr)
	return ok && constructor.Sel.Name == "NewTimer" && isIdent(constructor.X, "time") &&
		durationOK && duration.Sel.Name == "Second" && isIdent(duration.X, "time")
}

func isDeferStop(stmt ast.Stmt, receiver string) bool {
	deferStmt, ok := stmt.(*ast.DeferStmt)
	if !ok || len(deferStmt.Call.Args) != 0 {
		return false
	}
	stop, ok := deferStmt.Call.Fun.(*ast.SelectorExpr)
	return ok && stop.Sel.Name == "Stop" && isIdent(stop.X, receiver)
}

func selectWaitsForFinishedOrTimer(stmt ast.Stmt) bool {
	selectStmt, ok := stmt.(*ast.SelectStmt)
	if !ok || len(selectStmt.Body.List) != 2 {
		return false
	}
	finished, timer := false, false
	for _, rawClause := range selectStmt.Body.List {
		clause, ok := rawClause.(*ast.CommClause)
		if !ok || clause.Comm == nil {
			return false
		}
		expr, ok := clause.Comm.(*ast.ExprStmt)
		if !ok {
			return false
		}
		receive, ok := expr.X.(*ast.UnaryExpr)
		if !ok || receive.Op != token.ARROW {
			return false
		}
		finished = finished || isIdent(receive.X, "finished")
		if channel, ok := receive.X.(*ast.SelectorExpr); ok {
			timer = timer || channel.Sel.Name == "C" && isIdent(channel.X, "timer")
		}
	}
	return finished && timer
}

func isDeferClose(stmt ast.Stmt, channel string) bool {
	deferStmt, ok := stmt.(*ast.DeferStmt)
	if !ok || len(deferStmt.Call.Args) != 1 {
		return false
	}
	closeFn, ok := deferStmt.Call.Fun.(*ast.Ident)
	return ok && closeFn.Name == "close" && isIdent(deferStmt.Call.Args[0], channel)
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func isIntLiteral(expr ast.Expr, value string) bool {
	lit, ok := expr.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == value
}
