package wingd_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestSuccessorGraceRegressionsRunInParallel(t *testing.T) {
	t.Parallel()

	targets := map[string]bool{
		"TestChurn_HolderWatchReattachesAcrossKill": false,
		"TestCancel_ReattachedHolderIsCancellable":  false,
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
			if _, ok := targets[fn.Name.Name]; !ok {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Parallel" {
					return true
				}
				receiver, ok := sel.X.(*ast.Ident)
				if ok && receiver.Name == "t" && len(call.Args) == 0 {
					targets[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	for name, parallel := range targets {
		if !parallel {
			t.Errorf("%s must call t.Parallel so the independent successor-grace windows overlap", name)
		}
	}
}
