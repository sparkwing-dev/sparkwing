package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The concurrency subsystem keeps each invariant at one definition;
// these guards make a bypassing site fail the suite instead of
// compiling quietly. When one trips, route the new code through the
// named helper rather than relaxing the count.
func TestConcurrencyGuard_CanonicalSQLSitesOnly(t *testing.T) {
	src := storePackageSource(t)
	for needle, helper := range map[string]string{
		"INSERT INTO concurrency_holders": "txInsertHolder",
		"INSERT INTO concurrency_waiters": "txPark",
		"INSERT INTO concurrency_cache":   "txReleaseHolder",
		"DELETE FROM concurrency_waiters": "txDeleteWaiter",
		"SET superseded = 1":              "txSupersede",
		"lease_expires_at > ?":            "holderLiveSQL",
		"superseded = 0 AND ":             "holderLiveSQL",
	} {
		if got := strings.Count(src, needle); got != 1 {
			t.Errorf("%q appears %d times in pkg/store sources, want exactly 1 (inside %s)", needle, got, helper)
		}
	}
	// txDeleteHolder (by id) plus CancelWaiter's by-participant reclaim.
	if got := strings.Count(src, "DELETE FROM concurrency_holders"); got != 2 {
		t.Errorf("%q appears %d times in pkg/store sources, want exactly 2 (txDeleteHolder + CancelWaiter)", "DELETE FROM concurrency_holders", got)
	}
}

func TestConcurrencyGuard_TablesTouchedOnlyByStore(t *testing.T) {
	root := moduleRoot(t)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == "dist" || name == "web" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if strings.HasPrefix(path, filepath.Join(root, "pkg", "store")+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		s := string(b)
		for _, form := range []string{"FROM concurrency_", "INTO concurrency_", "UPDATE concurrency_"} {
			if strings.Contains(s, form) {
				t.Errorf("%s contains %q: concurrency tables may only be touched by pkg/store", path, form)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every mutating concurrency transaction must commit through
// txCommitChecked so the invariant checks cannot be skipped. Read-only
// paths are allowlisted; extending the list is a conscious act.
func TestConcurrencyGuard_MutatingCommitsAreChecked(t *testing.T) {
	allowed := map[string]bool{
		"txCommitChecked":     true,
		"ResolveWaiter":       true,
		"GetConcurrencyState": true,
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join("..", "store", "concurrency.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || allowed[fn.Name.Name] {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Commit" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && id.Name == "tx" {
				t.Errorf("%s commits directly at %s; mutating paths must use txCommitChecked",
					fn.Name.Name, fset.Position(sel.Pos()))
			}
			return true
		})
	}
}

func storePackageSource(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		src, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		b.Write(src)
	}
	return b.String()
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test working directory")
		}
		dir = parent
	}
}
