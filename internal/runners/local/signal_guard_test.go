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

// nodeEntrypoints are the two files where a node process installs its
// signal handling: the pipeline binary's `run-node` and the CLI's.
var nodeEntrypoints = []string{
	filepath.Join("internal", "orchestrator", "run_node.go"),
	filepath.Join("cmd", "sparkwing", "run_node.go"),
}

// TestNodeEntrypoints_DoNotHandleSIGTERM pins the invariant every kill
// path in this package rests on.
//
// Stopping a node's process means SIGTERM to its group: a bounce, a
// cancelled run, and a pod's own termination all do it. What makes
// those three distinguishable afterwards is that the child writes
// nothing on its way out -- the supervisor reads the node row, sees no
// terminal outcome, and applies the meaning it alone knows. Install a
// SIGTERM handler in either entrypoint and a bounced node can record
// an outcome mid-bounce, which is exactly the cascade a bounce exists
// to avoid.
//
// The check is on the source rather than on a running process because
// the regression is someone adding syscall.SIGTERM to a
// signal.Notify* call, and that is visible here before it can ever be
// executed.
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

// signalName renders an argument's source form well enough to spot a
// SIGTERM registered under any of its spellings.
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

// repoRoot resolves the repository root from this file's own path.
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
