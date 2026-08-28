package local

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var nodeEntrypoints = []string{
	filepath.Join("internal", "orchestrator", "run_node.go"),
	filepath.Join("cmd", "sparkwing", "run_node.go"),
}

func TestNodeEntrypoints_DoNotHandleSIGTERM(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range nodeEntrypoints {
		path := filepath.Join(root, rel)
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		var sawNotify bool
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "signal" {
				return true
			}
			if sel.Sel.Name != "Notify" && sel.Sel.Name != "NotifyContext" {
				return true
			}
			sawNotify = true
			for _, arg := range call.Args {
				if named := signalName(arg); strings.Contains(named, "SIGTERM") {
					t.Errorf("%s:%d: %s registers %s; a node process must die on SIGTERM without writing a terminal row",
						rel, fset.Position(arg.Pos()).Line, sel.Sel.Name, named)
				}
			}
			return true
		})
		if !sawNotify {
			t.Errorf("%s registers no signal handling at all; this guard is watching the wrong file", rel)
		}
	}
}

func signalName(arg ast.Expr) string {
	switch v := arg.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		if pkg, ok := v.X.(*ast.Ident); ok {
			return pkg.Name + "." + v.Sel.Name
		}
		return v.Sel.Name
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve this test's source path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	return root
}
