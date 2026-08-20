package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestProcessSuccessorObservesLeaseWithoutTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "process_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var foundHelper bool
	var foundRegression bool
	var holderReadyPos, observationPos token.Pos
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
			foundRegression = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				if isTimeSleep(node) {
					t.Errorf("%s: observe successor lease state directly", fset.Position(node.Pos()))
				}
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				switch ident.Name {
				case "waitForHolder":
					holderReadyPos = call.Pos()
				case "observeReattachedHolderFor":
					if len(call.Args) == 4 && is750Milliseconds(call.Args[3]) {
						observationPos = call.Pos()
					}
				}
				return true
			})
		}
	}
	if !foundHelper {
		t.Error("required declaration waitForDaemonLineCount not found")
	}
	if !foundRegression {
		t.Error("required declaration TestProcess_DaemonKillRestoresAndReattaches not found")
	}
	if !holderReadyPos.IsValid() || !observationPos.IsValid() || holderReadyPos >= observationPos {
		t.Error("successor regression must find then observe holder a for 750*time.Millisecond")
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

func is750Milliseconds(expr ast.Expr) bool {
	mul, ok := expr.(*ast.BinaryExpr)
	if !ok || mul.Op != token.MUL {
		return false
	}
	value, ok := mul.X.(*ast.BasicLit)
	if !ok || value.Value != "750" {
		return false
	}
	sel, ok := mul.Y.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Millisecond" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}
