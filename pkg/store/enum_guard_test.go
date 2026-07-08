package store_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// enumGroups freezes the value sets of the enums that scattered switch
// statements branch over. Adding a value to one of these enums fails
// this test on purpose: the failure message inventories every switch in
// the module that branches on the group, so the new value is carried
// into each of them (or consciously left to a default arm) instead of
// being silently absorbed. After reviewing the switches, add the new
// value here.
var enumGroups = map[string]struct {
	file   string
	values []string
}{
	"sparkwing.Outcome": {
		file: "sparkwing/outcome.go",
		values: []string{
			"Success", "Failed", "Satisfied", "Cached", "Skipped",
			"Cancelled", "SkippedConcurrent", "Superseded",
		},
	},
	"store.AcquireKind": {
		file: "pkg/store/concurrency.go",
		values: []string{
			"AcquireGranted", "AcquireQueued", "AcquireCoalesced",
			"AcquireSkipped", "AcquireFailed", "AcquireCached",
			"AcquireCancellingOthers",
		},
	},
	"store.WaiterStatus": {
		file: "pkg/store/concurrency.go",
		values: []string{
			"WaiterStillWaiting", "WaiterPromoted", "WaiterCached",
			"WaiterLeaderFinished", "WaiterCancelled",
		},
	},
	"store.OnLimit": {
		file: "pkg/store/concurrency.go",
		values: []string{
			"OnLimitQueue", "OnLimitCoalesce", "OnLimitSkip",
			"OnLimitFail", "OnLimitCancelOthers",
		},
	},
}

func TestEnumGuard_SwitchedEnumSetsAreAcknowledged(t *testing.T) {
	root := moduleRoot(t)
	for group, want := range enumGroups {
		got := declaredConstNames(t, filepath.Join(root, want.file), want.values)
		missing, extra := diffSets(want.values, got)
		if len(missing) == 0 && len(extra) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s changed: removed=%v added=%v\n", group, missing, extra)
		b.WriteString("review every switch over this group, then update enumGroups:\n")
		for _, site := range switchSites(t, root, want.values) {
			fmt.Fprintf(&b, "  %s\n", site)
		}
		t.Error(b.String())
	}
}

// declaredConstNames returns the names from file's top-level const
// blocks that either carry one of the group's known names or share its
// declared type with one that does, so a freshly added value in the
// same block is picked up.
func declaredConstNames(t *testing.T, file string, knownNames []string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, n := range knownNames {
		known[n] = true
	}
	var out []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		blockHasKnown := false
		var names []string
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, id := range vs.Names {
				names = append(names, id.Name)
				if known[id.Name] {
					blockHasKnown = true
				}
			}
		}
		if blockHasKnown {
			out = append(out, names...)
		}
	}
	return out
}

func diffSets(want, got []string) (missing, extra []string) {
	w := map[string]bool{}
	g := map[string]bool{}
	for _, s := range want {
		w[s] = true
	}
	for _, s := range got {
		g[s] = true
	}
	for _, s := range want {
		if !g[s] {
			missing = append(missing, s)
		}
	}
	for _, s := range got {
		if !w[s] {
			extra = append(extra, s)
		}
	}
	return missing, extra
}

// switchSites inventories every switch statement in the module whose
// case clauses reference at least one of the group's constant names,
// noting whether a default arm would silently absorb a new value.
func switchSites(t *testing.T, root string, values []string) []string {
	t.Helper()
	known := map[string]bool{}
	for _, v := range values {
		known[v] = true
	}
	var sites []string
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
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			hits := 0
			hasDefault := false
			for _, c := range sw.Body.List {
				cc, ok := c.(*ast.CaseClause)
				if !ok {
					continue
				}
				if cc.List == nil {
					hasDefault = true
					continue
				}
				for _, e := range cc.List {
					ast.Inspect(e, func(en ast.Node) bool {
						switch x := en.(type) {
						case *ast.Ident:
							if known[x.Name] {
								hits++
							}
						case *ast.SelectorExpr:
							if known[x.Sel.Name] {
								hits++
							}
							return false
						}
						return true
					})
				}
			}
			if hits > 0 {
				arm := "no default (new value falls through)"
				if hasDefault {
					arm = "default arm absorbs new values"
				}
				rel, _ := filepath.Rel(root, path)
				sites = append(sites, fmt.Sprintf("%s:%d (%s)", rel, fset.Position(sw.Pos()).Line, arm))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sites)
	return sites
}
