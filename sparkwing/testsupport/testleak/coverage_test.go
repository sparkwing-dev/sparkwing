package testleak

import (
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestEveryTestPackageRunsInsideTheSandbox(t *testing.T) {
	root := moduleRoot(t)
	dirs := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (name == "testdata" || name == "vendor" || name == "node_modules" ||
				strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || hasGoMod(path)) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			dirs[filepath.Dir(path)] = append(dirs[filepath.Dir(path)], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	var missing []string
	for dir, files := range dirs {
		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		// safety: a TestMain behind a build constraint protects only the
		// platforms that compile it, so each target is judged on its own files.
		for _, goos := range []string{"linux", "darwin", "windows"} {
			selected := targetFiles(t, goos, files)
			if len(selected) > 0 && !testMainUsesTestleak(t, selected) {
				missing = append(missing, rel+" ("+goos+")")
			}
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these test packages run without the sandbox home, so a test that forgets its own "+
			"home reaches the developer's real files. Add a leak_test.go with "+
			"`func TestMain(m *testing.M) { testleak.Main(m) }`:\n  %s", strings.Join(missing, "\n  "))
	}
}

// targetFiles keeps the files a default build for goos compiles.
func targetFiles(t *testing.T, goos string, files []string) []string {
	t.Helper()
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH, ctx.BuildTags = goos, "amd64", nil
	var selected []string
	for _, path := range files {
		ok, err := ctx.MatchFile(filepath.Dir(path), filepath.Base(path))
		if err != nil {
			t.Fatalf("match %s: %v", path, err)
		}
		if ok {
			selected = append(selected, path)
		}
	}
	return selected
}

func testMainUsesTestleak(t *testing.T, files []string) bool {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "TestMain" || fn.Body == nil {
				continue
			}
			if callsTestleak(fn.Body, file.Name.Name == "testleak") {
				return true
			}
		}
	}
	return false
}

func callsTestleak(body *ast.BlockStmt, inPackage bool) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return !found
		}
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := fun.X.(*ast.Ident); ok && pkg.Name == "testleak" && isEntry(fun.Sel.Name) {
				found = true
			}
		case *ast.Ident:
			if inPackage && isEntry(fun.Name) {
				found = true
			}
		}
		return !found
	})
	return found
}

func isEntry(name string) bool { return name == "Main" || name == "Check" }

func hasGoMod(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "go.mod"))
	return err == nil
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for !hasGoMod(dir) {
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
	return dir
}
