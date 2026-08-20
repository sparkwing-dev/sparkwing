package chaos

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func TestCrashdummyChildFixtureUsesObservedReleaseSignal(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "child_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var target *ast.FuncDecl
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name.Name == "TestCrashdummy_ChildrenAttachToParentLease" {
			target = fn
			break
		}
	}
	if target == nil {
		t.Fatal("TestCrashdummy_ChildrenAttachToParentLease declaration missing")
	}

	var familyObservedAt, releaseAt, convergeAt token.Pos
	indefiniteHold := false
	waitsInWorker := false
	ast.Inspect(target.Body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				switch id.Name {
				case "sawFamily":
					if i < len(node.Rhs) {
						if value, ok := node.Rhs[i].(*ast.Ident); ok && value.Name == "true" {
							familyObservedAt = node.Pos()
						}
					}
				case "convergeCtx":
					convergeAt = node.Pos()
				}
			}
		case *ast.GoStmt:
			ast.Inspect(node.Call, func(child ast.Node) bool {
				call, ok := child.(*ast.CallExpr)
				if !ok {
					return true
				}
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Wait" {
					if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "parent" {
						waitsInWorker = true
					}
				}
				return true
			})
		case *ast.CallExpr:
			if fn, ok := node.Fun.(*ast.SelectorExpr); ok && fn.Sel.Name == "Signal" {
				if process, ok := fn.X.(*ast.SelectorExpr); ok && process.Sel.Name == "Process" {
					if parent, ok := process.X.(*ast.Ident); ok && parent.Name == "parent" {
						releaseAt = node.Pos()
					}
				}
			}
			fn, ok := node.Fun.(*ast.SelectorExpr)
			if !ok || fn.Sel.Name != "Command" {
				return true
			}
			pkg, ok := fn.X.(*ast.Ident)
			if !ok || pkg.Name != "exec" {
				return true
			}
			for i := 0; i+1 < len(node.Args); i++ {
				flag, flagOK := node.Args[i].(*ast.BasicLit)
				value, valueOK := node.Args[i+1].(*ast.BasicLit)
				if flagOK && valueOK && flag.Value == strconv.Quote("--run-ms") && value.Value == strconv.Quote("0") {
					indefiniteHold = true
				}
			}
		}
		return true
	})
	if !indefiniteHold {
		t.Error("parent fixture must hold indefinitely until the test releases it")
	}
	if familyObservedAt == token.NoPos || releaseAt <= familyObservedAt || convergeAt <= releaseAt {
		t.Error("parent release must follow complete-family observation and precede convergence")
	}
	if !waitsInWorker {
		t.Error("parent wait must run in its lifecycle worker")
	}
}

func TestCrashdummyHolderInstallsSignalsBeforeSpawningChildren(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "crashdummy/main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var installAt, spawnAt token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" || fn.Recv == nil {
			return true
		}
		ast.Inspect(fn.Body, func(child ast.Node) bool {
			call, ok := child.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			receiver, receiverOK := sel.X.(*ast.Ident)
			if !receiverOK || receiver.Name != "h" {
				return true
			}
			switch sel.Sel.Name {
			case "installSignals":
				installAt = call.Pos()
			case "spawnChildren":
				spawnAt = call.Pos()
			}
			return true
		})
		return false
	})
	if installAt == token.NoPos || spawnAt == token.NoPos || installAt >= spawnAt {
		t.Fatal("holder must install signal handling before children can publish readiness")
	}
}
