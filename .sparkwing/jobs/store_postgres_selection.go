package jobs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// perf: the full Go suite already runs the internal package's SQLite-only
// tests and fuzz seeds, so this job keeps the external package, whose tests
// open through storetest, and the internal tests that reach the server.
const (
	storePostgresPackage    = "./internal/store"
	storePostgresPackageDir = "internal/store"
	storePostgresURLEnv     = "SPARKWING_TEST_PG_URL"
)

// perf: both names run one helper; under SPARKWING_TEST_STORE=postgres the
// dialect-following copy repeats the PostgreSQL one, so only the latter runs.
var storePostgresDuplicates = map[string]string{
	"TestClaimSecurityRegressions": "TestPostgresClaimSecurityRegressions",
}

type storePostgresSelection struct {
	run, skipped []string
}

type storeTestDecl struct {
	external, postgres bool
}

func selectStorePostgresTests(listed []string, packageDir string) (storePostgresSelection, error) {
	decls, err := storeTestDeclarations(packageDir)
	if err != nil {
		return storePostgresSelection{}, err
	}
	var sel storePostgresSelection
	for _, name := range listed {
		decl, ok := decls[name]
		if !ok {
			return storePostgresSelection{}, fmt.Errorf("listed test %q has no declaration in %s/*_test.go, so the postgres selection cannot place it", name, packageDir)
		}
		if _, duplicate := storePostgresDuplicates[name]; duplicate {
			sel.skipped = append(sel.skipped, name)
			continue
		}
		if decl.external || decl.postgres {
			sel.run = append(sel.run, name)
		} else {
			sel.skipped = append(sel.skipped, name)
		}
	}
	for duplicate, kept := range storePostgresDuplicates {
		if !slices.Contains(listed, duplicate) || !slices.Contains(sel.run, kept) {
			return storePostgresSelection{}, fmt.Errorf("postgres duplicate %q or the copy it defers to, %q, is no longer listed; update storePostgresDuplicates", duplicate, kept)
		}
	}
	if len(sel.run) == 0 {
		return storePostgresSelection{}, fmt.Errorf("no listed test in %s exercises postgres", packageDir)
	}
	return sel, checkStorePostgresPartition(listed, sel)
}

func checkStorePostgresPartition(listed []string, sel storePostgresSelection) error {
	placed := make(map[string]int, len(listed))
	for _, name := range append(slices.Clone(sel.run), sel.skipped...) {
		placed[name]++
	}
	for _, name := range listed {
		if placed[name] != 1 {
			return fmt.Errorf("listed test %q is placed %d times by the postgres selection, want once", name, placed[name])
		}
		delete(placed, name)
	}
	for name := range placed {
		return fmt.Errorf("postgres selection placed unlisted test %q", name)
	}
	return nil
}

// safety: the graph spans the internal package's test files, so a test reaches
// the server through a helper or constant as well as directly; a reference
// to any same-named declaration counts, which can only add tests.
func storeTestDeclarations(packageDir string) (map[string]storeTestDecl, error) {
	entries, err := os.ReadDir(packageDir)
	if err != nil {
		return nil, fmt.Errorf("read %s for the postgres selection: %w", packageDir, err)
	}
	fset := token.NewFileSet()
	decls := map[string]storeTestDecl{}
	refs := map[string]map[string]bool{}
	direct := map[string]bool{}
	var internal []string
	note := func(name string, node ast.Node) {
		if refs[name] == nil {
			refs[name] = map[string]bool{}
		}
		ast.Inspect(node, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				refs[name][n.Name] = true
			case *ast.BasicLit:
				if n.Kind == token.STRING {
					if value, err := strconv.Unquote(n.Value); err == nil && (strings.Contains(value, storePostgresURLEnv) || value == "pgx") {
						direct[name] = true
					}
				}
			}
			return true
		})
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(packageDir, entry.Name()), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s for the postgres selection: %w", entry.Name(), err)
		}
		external := strings.HasSuffix(file.Name.Name, "_test")
		for _, decl := range file.Decls {
			switch decl := decl.(type) {
			case *ast.FuncDecl:
				name := decl.Name.Name
				if decl.Recv == nil && isTopLevelTestName(name) {
					d := decls[name]
					d.external = d.external || external
					decls[name] = d
					if !external {
						internal = append(internal, name)
					}
				}
				if !external {
					note(name, decl)
				}
			case *ast.GenDecl:
				if external {
					continue
				}
				for _, spec := range decl.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, ident := range value.Names {
							note(ident.Name, value)
						}
					}
				}
			}
		}
	}
	reaches := map[string]bool{}
	var visit func(string, map[string]bool) bool
	visit = func(name string, seen map[string]bool) bool {
		if direct[name] || reaches[name] {
			return true
		}
		if seen[name] {
			return false
		}
		seen[name] = true
		for ref := range refs[name] {
			if ref != name && refs[ref] != nil && visit(ref, seen) {
				reaches[name] = true
				return true
			}
		}
		return false
	}
	for _, name := range internal {
		if visit(name, map[string]bool{}) {
			d := decls[name]
			d.postgres = true
			decls[name] = d
		}
	}
	return decls, nil
}

func isTopLevelTestName(name string) bool {
	if name == "TestMain" {
		return false
	}
	return strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Fuzz") || strings.HasPrefix(name, "Example")
}
