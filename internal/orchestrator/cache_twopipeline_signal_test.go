package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSharedS3SerializationUsesStateSignalsInsteadOfDuration(t *testing.T) {
	targets := map[string]bool{
		"s3Push":           false,
		"runSharedS3Burst": false,
		"TestCache_TwoPipelinesShareKey_PushSerializes":       false,
		"TestCache_TwoPipelinesShareKey_AcrossMultipleBursts": false,
	}
	usesBurst := map[string]bool{}
	exactPopulationSequence := false
	signals := map[string]map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cache_twopipeline_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
		signals[fn.Name.Name] = map[string]bool{}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "runSharedS3Burst" {
				usesBurst[fn.Name.Name] = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" && (sel.Sel.Name == "Sleep" || sel.Sel.Name == "After" || sel.Sel.Name == "NewTicker") {
				t.Errorf("%s contains time.%s at %s", fn.Name.Name, sel.Sel.Name, fset.Position(call.Pos()))
			}
			return true
		})
		if fn.Name.Name == "runSharedS3Burst" {
			for _, stmt := range fn.Body.List {
				loop, ok := stmt.(*ast.ForStmt)
				if !ok || len(loop.Body.List) != 6 {
					continue
				}
				exactPopulationSequence = selectReceives(loop.Body.List[0], "entered") &&
					exactPopulationCall(loop.Body.List[1]) &&
					selectRejectsExtraEntry(loop.Body.List[2]) &&
					selectSends(loop.Body.List[4], "release") &&
					selectReceives(loop.Body.List[5], "finished")
			}
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if ok && (sel.Sel.Name == "entered" || sel.Sel.Name == "release" || sel.Sel.Name == "finished") {
				signals[fn.Name.Name][sel.Sel.Name] = true
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_twopipeline_test.go does not declare %s", name)
		}
	}
	for _, name := range []string{
		"TestCache_TwoPipelinesShareKey_PushSerializes",
		"TestCache_TwoPipelinesShareKey_AcrossMultipleBursts",
	} {
		if !usesBurst[name] {
			t.Errorf("%s does not delegate to runSharedS3Burst", name)
		}
	}
	if !exactPopulationSequence {
		t.Error("runSharedS3Burst must order entry, exact persisted population, false-start check, release, and finish directly in its admission loop")
	}
	for _, name := range []string{"s3Push", "runSharedS3Burst"} {
		for _, signal := range []string{"entered", "release", "finished"} {
			if !signals[name][signal] {
				t.Errorf("%s does not use the gate's %s signal", name, signal)
			}
		}
	}
}

func selectReceives(stmt ast.Stmt, channel string) bool {
	selectStmt, ok := stmt.(*ast.SelectStmt)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(selectStmt, func(node ast.Node) bool {
		receive, ok := node.(*ast.UnaryExpr)
		if !ok || receive.Op != token.ARROW {
			return true
		}
		sel, ok := receive.X.(*ast.SelectorExpr)
		found = found || ok && sel.Sel.Name == channel && isIdent(sel.X, "gate")
		return true
	})
	return found
}

func selectRejectsExtraEntry(stmt ast.Stmt) bool {
	selectStmt, ok := stmt.(*ast.SelectStmt)
	if !ok || len(selectStmt.Body.List) != 2 {
		return false
	}
	hasEntered, hasDefault := false, false
	for _, rawClause := range selectStmt.Body.List {
		clause, ok := rawClause.(*ast.CommClause)
		if !ok {
			return false
		}
		if clause.Comm == nil {
			hasDefault = true
			continue
		}
		expr, ok := clause.Comm.(*ast.ExprStmt)
		if !ok {
			continue
		}
		receive, ok := expr.X.(*ast.UnaryExpr)
		if !ok || receive.Op != token.ARROW {
			continue
		}
		sel, ok := receive.X.(*ast.SelectorExpr)
		hasEntered = hasEntered || ok && sel.Sel.Name == "entered" && isIdent(sel.X, "gate")
	}
	return hasEntered && hasDefault
}

func selectSends(stmt ast.Stmt, channel string) bool {
	selectStmt, ok := stmt.(*ast.SelectStmt)
	if !ok {
		return false
	}
	found := false
	ast.Inspect(selectStmt, func(node ast.Node) bool {
		send, ok := node.(*ast.SendStmt)
		if !ok {
			return true
		}
		sel, ok := send.Chan.(*ast.SelectorExpr)
		found = found || ok && sel.Sel.Name == channel && isIdent(sel.X, "gate")
		return true
	})
	return found
}

func exactPopulationCall(stmt ast.Stmt) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 6 {
		return false
	}
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || fun.Name != "waitForCacheConcurrencyPopulation" || !isIdent(call.Args[0], "t") || !isIdent(call.Args[1], "ctx") {
		return false
	}
	stateDB, ok := call.Args[2].(*ast.CallExpr)
	if !ok || len(stateDB.Args) != 0 {
		return false
	}
	stateSel, ok := stateDB.Fun.(*ast.SelectorExpr)
	if !ok || stateSel.Sel.Name != "StateDB" || !isIdent(stateSel.X, "p") {
		return false
	}
	key, ok := call.Args[3].(*ast.BasicLit)
	if !ok || key.Kind != token.STRING || key.Value != `"g:shared-s3-bucket"` {
		return false
	}
	holders, ok := call.Args[4].(*ast.BasicLit)
	return ok && holders.Kind == token.INT && holders.Value == "1" && isRemainingWaiters(call.Args[5])
}

func isRemainingWaiters(expr ast.Expr) bool {
	outer, ok := expr.(*ast.BinaryExpr)
	if !ok || outer.Op != token.SUB || !isIntLiteral(outer.Y, "1") {
		return false
	}
	inner, ok := outer.X.(*ast.BinaryExpr)
	if !ok || inner.Op != token.SUB || !isIdent(inner.Y, "admitted") {
		return false
	}
	length, ok := inner.X.(*ast.CallExpr)
	return ok && len(length.Args) == 1 && isIdent(length.Fun, "len") && isIdent(length.Args[0], "names")
}

func isIdent(expr ast.Expr, name string) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == name
}

func isIntLiteral(expr ast.Expr, value string) bool {
	lit, ok := expr.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == value
}
