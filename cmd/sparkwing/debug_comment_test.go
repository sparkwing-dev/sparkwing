package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestDebugSourceDoesNotClaimRetiredProfileFlagWorks(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "debug.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range file.Comments {
		if strings.Contains(group.Text(), "--sw-profile") {
			t.Errorf("debug.go advertises retired --sw-profile at %s", fset.Position(group.Pos()))
		}
	}
}
