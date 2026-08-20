package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestXrepoRuntimeHelpRunsInParallel(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "xrepo_help_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestXrepoRuntimeHelpUsesCommandRegistry" {
			continue
		}
		found = true
		if !firstStatementIsParallel(fn) {
			t.Fatal("TestXrepoRuntimeHelpUsesCommandRegistry must call t.Parallel() first")
		}
	}
	if !found {
		t.Fatal("TestXrepoRuntimeHelpUsesCommandRegistry declaration is missing")
	}
}
