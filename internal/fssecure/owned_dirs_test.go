package fssecure_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var configDirPackages = map[string]bool{
	"internal/fssecure": true,
	"internal/paths":    true,
	"internal/profile":  true,
	"internal/repos":    true,
	"internal/secrets":  true,
}

var configDirProviders = map[string]bool{
	"profile.DefaultPath":       true,
	"repos.DefaultPath":         true,
	"secrets.DefaultConfigPath": true,
	"secrets.DefaultDotenvPath": true,
}

func TestConfigDirectoryWritersDoNotMkdirGroupReadable(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	scanned, scoped := 0, 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if path != root && skipScanDir(path, d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil
		}
		scanned++
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if !configDirPackages[filepath.ToSlash(filepath.Dir(rel))] && !resolvesConfigDir(file) {
			return nil
		}
		scoped++
		reportLooseMkdirs(t, fset, file, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if scanned == 0 || scoped == 0 {
		t.Fatalf("scanned %d files and matched %d config-directory writers, so this check proves nothing", scanned, scoped)
	}
}

func skipScanDir(path, name string) bool {
	switch name {
	case "node_modules", "vendor", "testdata":
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	_, err := os.Stat(filepath.Join(path, "go.mod"))
	return err == nil
}

func resolvesConfigDir(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		if configDirProviders[pkg.Name+"."+sel.Sel.Name] {
			found = true
			return false
		}
		return true
	})
	return found
}

func reportLooseMkdirs(t *testing.T, fset *token.FileSet, file *ast.File, rel string) {
	t.Helper()
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "MkdirAll" && sel.Sel.Name != "Mkdir") {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || pkg.Name != "os" {
			return true
		}
		perm, ok := literalPerm(call.Args[1])
		if !ok || perm&0o077 == 0 {
			return true
		}
		t.Errorf("%s:%d: os.%s with mode %#o leaves the sparkwing config directory reachable by group or other users; "+
			"use fssecure.EnsureDir instead", rel, fset.Position(call.Pos()).Line, sel.Sel.Name, perm)
		return true
	})
}

func literalPerm(arg ast.Expr) (uint64, bool) {
	lit, ok := arg.(*ast.BasicLit)
	if !ok || lit.Kind != token.INT {
		return 0, false
	}
	value, err := strconv.ParseUint(lit.Value, 0, 32)
	if err != nil {
		return 0, false
	}
	return value, true
}
