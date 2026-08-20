package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestIndependentProcessRegressionsRunInParallel(t *testing.T) {
	t.Parallel()

	targets := map[string]bool{
		"TestProcess_ElectionRaceSingleDaemon":      false,
		"TestProcess_ClientKillReleasesAndPromotes": false,
	}
	fset := token.NewFileSet()
	paths, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatalf("glob test files: %v", err)
	}
	for _, path := range paths {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, ok := targets[fn.Name.Name]; !ok || len(fn.Body.List) == 0 {
				continue
			}
			expr, ok := fn.Body.List[0].(*ast.ExprStmt)
			if !ok {
				continue
			}
			call, ok := expr.X.(*ast.CallExpr)
			if !ok || len(call.Args) != 0 {
				continue
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Parallel" {
				continue
			}
			receiver, ok := sel.X.(*ast.Ident)
			targets[fn.Name.Name] = ok && receiver.Name == "t"
		}
	}
	for name, parallel := range targets {
		if !parallel {
			t.Errorf("%s must call t.Parallel so independent process waits overlap", name)
		}
	}
}
