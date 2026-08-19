package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestHolderKeepsMemoryBallastAliveThroughHoldLoop(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "main.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	body := holderRunBody(file)
	if body == nil {
		t.Fatal("holder run method not found")
	}
	for i := 0; i+1 < len(body.List); i++ {
		if isSelectorCall(body.List[i], "h", "holdLoop", "") &&
			isSelectorCall(body.List[i+1], "runtime", "KeepAlive", "ballast") {
			return
		}
	}
	t.Fatal("holder run method does not keep memory ballast alive immediately after its hold loop")
}

func holderRunBody(file *ast.File) *ast.BlockStmt {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "run" || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		ident, ok := star.X.(*ast.Ident)
		if ok && ident.Name == "holder" {
			return fn.Body
		}
	}
	return nil
}

func isSelectorCall(stmt ast.Stmt, receiver, method, arg string) bool {
	expr, ok := stmt.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	recv, ok := sel.X.(*ast.Ident)
	if !ok || recv.Name != receiver || sel.Sel.Name != method {
		return false
	}
	if arg == "" {
		return len(call.Args) == 0
	}
	if len(call.Args) != 1 {
		return false
	}
	ident, ok := call.Args[0].(*ast.Ident)
	return ok && ident.Name == arg
}
