package local

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var nodeEntrypoints = []string{
	filepath.Join("internal", "orchestrator", "run_node.go"),
	filepath.Join("internal", "orchestrator", "run_node_verb.go"),
}

var nodeDispatchers = []string{
	filepath.Join("cmd", "sparkwing", "main.go"),
	filepath.Join("internal", "cluster", "main.go"),
}

func TestNodeEntrypoints_DoNotHandleSIGTERM(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range nodeEntrypoints {
		if !signalHandling(t, root, rel) {
			t.Errorf("%s registers no signal handling at all; this guard is watching the wrong file", rel)
		}
	}
}

func TestNodeDispatchers_AddNoSignalHandling(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range nodeDispatchers {
		if signalHandling(t, root, rel) {
			t.Errorf("%s handles signals itself; a run-node dispatcher must leave that to the shared entrypoint", rel)
		}
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(body), "RunNodeCommand") {
			t.Errorf("%s no longer dispatches run-node to the shared entrypoint; this guard is watching the wrong file", rel)
		}
	}
}

func signalHandling(t *testing.T, root, rel string) bool {
	t.Helper()
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
	return sawNotify
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

func moduleRootDir(t *testing.T) string {
	t.Helper()
	// safety: a reproducible build trims the compiled-in source path, so resolving the
	// repository from runtime.Caller yields a module path the filesystem does not hold.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("resolve working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	return moduleRootDir(t)
}
