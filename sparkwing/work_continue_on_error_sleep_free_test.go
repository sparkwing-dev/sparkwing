package sparkwing

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestWorkFailureHandlingRegressionsDoNotSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_continue_on_error_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
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
			t.Errorf("%s: synchronize sibling entry directly", fset.Position(call.Pos()))
		}
		return true
	})
}

func TestDefaultFailFastObservesSiblingCancellationDirectly(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "work_continue_on_error_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "TestRunWork_DefaultFailFastCancelsSiblings" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("TestRunWork_DefaultFailFastCancelsSiblings declaration missing")
	}
	const wantSlow = `func(ctx context.Context) error {
	close(siblingEntered)
	<-ctx.Done()
	siblingCancelled.Store(true)
	return ctx.Err()
}`
	foundSlow := false
	usesManualCancel := false
	runWorkLaunched := false
	watchdogOwned := false
	watchdogReceived := false
	ast.Inspect(target.Body, func(node ast.Node) bool {
		if goStmt, ok := node.(*ast.GoStmt); ok {
			ast.Inspect(goStmt.Call, func(child ast.Node) bool {
				call, ok := child.(*ast.CallExpr)
				fn, fnOK := call.Fun.(*ast.Ident)
				if ok && fnOK && fn.Name == "RunWork" {
					runWorkLaunched = true
				}
				return true
			})
		}
		if unary, ok := node.(*ast.UnaryExpr); ok && unary.Op == token.ARROW {
			if sel, ok := unary.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "C" {
				if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "watchdog" {
					watchdogReceived = true
				}
			}
		}
		assign, ok := node.(*ast.AssignStmt)
		if ok && len(assign.Lhs) == 1 && len(assign.Rhs) == 1 {
			lhs, lhsOK := assign.Lhs[0].(*ast.Ident)
			call, callOK := assign.Rhs[0].(*ast.CallExpr)
			if lhsOK && callOK {
				sel, selOK := call.Fun.(*ast.SelectorExpr)
				if selOK {
					pkg, pkgOK := sel.X.(*ast.Ident)
					if pkgOK && lhs.Name == "watchdog" && pkg.Name == "time" && sel.Sel.Name == "NewTimer" {
						watchdogOwned = true
					}
				}
			}
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && sel.Sel.Name == "After" {
				t.Errorf("%s: observe sibling cancellation directly", fset.Position(call.Pos()))
			}
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "context" {
				switch sel.Sel.Name {
				case "WithCancel":
					usesManualCancel = true
				case "WithTimeout", "WithDeadline":
					t.Errorf("%s: the run context deadline can impersonate fail-fast cancellation", fset.Position(call.Pos()))
				}
			}
		}
		fn, ok := call.Fun.(*ast.Ident)
		if !ok || fn.Name != "Step" || len(call.Args) < 3 {
			return true
		}
		name, ok := call.Args[1].(*ast.BasicLit)
		if !ok || name.Kind != token.STRING || name.Value != strconv.Quote("slow") {
			return true
		}
		body, ok := call.Args[2].(*ast.FuncLit)
		if !ok {
			t.Fatal("slow step callback is not a function literal")
		}
		var formatted bytes.Buffer
		if err := format.Node(&formatted, fset, body); err != nil {
			t.Fatal(err)
		}
		if got := formatted.String(); got != wantSlow {
			t.Errorf("slow step callback =\n%s\nwant direct cancellation callback =\n%s", got, wantSlow)
		}
		foundSlow = true
		return true
	})
	if !foundSlow {
		t.Fatal("default fail-fast regression slow step missing")
	}
	if !usesManualCancel || !runWorkLaunched || !watchdogOwned || !watchdogReceived {
		t.Fatal("default fail-fast regression must use a manual run cancellation and an independently watched RunWork goroutine")
	}
}
