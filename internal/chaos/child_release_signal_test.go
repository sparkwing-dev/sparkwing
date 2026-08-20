package chaos

import (
	"bytes"
	"go/ast"
	"go/format"
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

	var familyObservedAt, releaseAt token.Pos
	indefiniteHold := false
	waitsInWorker := false
	cleanupOwnsFamily := false
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
			if fn, ok := node.Fun.(*ast.Ident); ok && fn.Name == "stopCrashdummyFamily" && node.Pos() > releaseAt {
				releaseAt = node.Pos()
			}
			if fn, ok := node.Fun.(*ast.SelectorExpr); ok && fn.Sel.Name == "Signal" {
				if process, ok := fn.X.(*ast.SelectorExpr); ok && process.Sel.Name == "Process" {
					if parent, ok := process.X.(*ast.Ident); ok && parent.Name == "parent" {
						releaseAt = node.Pos()
					}
				}
			}
			fn, ok := node.Fun.(*ast.SelectorExpr)
			if ok && fn.Sel.Name == "Cleanup" {
				if receiver, ok := fn.X.(*ast.Ident); ok && receiver.Name == "t" && len(node.Args) == 1 {
					if cleanup, ok := node.Args[0].(*ast.FuncLit); ok {
						ast.Inspect(cleanup.Body, func(child ast.Node) bool {
							call, ok := child.(*ast.CallExpr)
							if !ok {
								return true
							}
							callee, calleeOK := call.Fun.(*ast.Ident)
							if calleeOK && callee.Name == "stopCrashdummyFamily" {
								cleanupOwnsFamily = true
							}
							return true
						})
					}
				}
			}
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
	if familyObservedAt == token.NoPos || releaseAt <= familyObservedAt {
		t.Error("bounded family release must follow complete-family observation")
	}
	if !waitsInWorker {
		t.Error("parent wait must run in its lifecycle worker")
	}
	if !cleanupOwnsFamily {
		t.Error("fatal cleanup must boundedly stop the complete crashdummy family")
	}
}

func TestCrashdummyHolderInstallsSignalsBeforeSpawningChildren(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "crashdummy/main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var installAt, ensureAt, spawnAt token.Pos
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
				if receiverOK && receiver.Name == "client" && sel.Sel.Name == "EnsureDaemon" {
					ensureAt = call.Pos()
				}
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
	if installAt == token.NoPos || ensureAt == token.NoPos || spawnAt == token.NoPos || installAt >= ensureAt || installAt >= spawnAt {
		t.Fatal("holder must install signal handling before admission or child readiness can become visible")
	}
}

func TestCrashdummyCleanExitReleasesSpawnedChildren(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "crashdummy/main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var terminateDecl *ast.FuncDecl
	directReleaseBeforeExit := false
	ast.Inspect(file, func(node ast.Node) bool {
		fn, ok := node.(*ast.FuncDecl)
		if !ok || fn.Recv == nil {
			return true
		}
		switch fn.Name.Name {
		case "cleanExit":
			var releaseAt, exitAt token.Pos
			for _, stmt := range fn.Body.List {
				expr, ok := stmt.(*ast.ExprStmt)
				if ok {
					call, ok := expr.X.(*ast.CallExpr)
					if ok {
						sel, ok := call.Fun.(*ast.SelectorExpr)
						if ok && sel.Sel.Name == "terminateChildren" {
							if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "h" {
								releaseAt = stmt.Pos()
							}
						}
					}
				}
				ast.Inspect(stmt, func(child ast.Node) bool {
					call, ok := child.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if ok && sel.Sel.Name == "Exit" {
						if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "os" && exitAt == token.NoPos {
							exitAt = call.Pos()
						}
					}
					return true
				})
			}
			directReleaseBeforeExit = releaseAt != token.NoPos && exitAt != token.NoPos && releaseAt < exitAt
		case "terminateChildren":
			terminateDecl = fn
		}
		return false
	})
	if !directReleaseBeforeExit {
		t.Error("holder clean exit must directly terminate children before any exit path")
	}
	if terminateDecl == nil {
		t.Fatal("terminateChildren declaration missing")
	}
	var formatted bytes.Buffer
	if err := format.Node(&formatted, fset, terminateDecl); err != nil {
		t.Fatal(err)
	}
	const want = `func (h *holder) terminateChildren() {
	h.mu.Lock()
	children := append([]*exec.Cmd(nil), h.children...)
	h.mu.Unlock()
	for _, child := range children {
		_ = child.Process.Signal(syscall.SIGTERM)
	}
}`
	if got := formatted.String(); got != want {
		t.Errorf("terminateChildren =\n%s\nwant =\n%s", got, want)
	}
}
