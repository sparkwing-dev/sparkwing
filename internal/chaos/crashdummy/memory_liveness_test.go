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

	var holdLoopPos, keepAlivePos token.Pos
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case receiver.Name == "h" && sel.Sel.Name == "holdLoop":
			holdLoopPos = call.Pos()
		case receiver.Name == "runtime" && sel.Sel.Name == "KeepAlive" && len(call.Args) == 1:
			arg, ok := call.Args[0].(*ast.Ident)
			if ok && arg.Name == "ballast" {
				keepAlivePos = call.Pos()
			}
		}
		return true
	})

	if holdLoopPos == token.NoPos {
		t.Fatal("holder does not enter its hold loop")
	}
	if keepAlivePos == token.NoPos || keepAlivePos < holdLoopPos {
		t.Fatal("memory ballast is not kept alive through the hold loop")
	}
}
