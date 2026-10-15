package store

import (
	"go/ast"
	"go/types"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const modulePath = "github.com/sparkwing-dev/sparkwing"

const (
	defaultTeamBackend = "the store backend serves one install's runs in the default team, as its ListRuns already does; a multi-team deployment serves its dashboard through the controller backend"
)

// safety: every entry names a function outside this package that reaches a
// Store method acting on the default team only, with the reason the default
// team is the one it means; a call nobody vouched for is how another team's
// rows go unread.
var defaultTeamCallers = map[string]string{
	"internal/backend.(*StoreBackend).GetRun":          defaultTeamBackend,
	"internal/backend.(*StoreBackend).ListEventsAfter": defaultTeamBackend,
	"internal/backend.(*StoreBackend).ListNodes":       defaultTeamBackend,
}

// The default-team twins are what keep the laptop store and the local paths
// working without a team; anything that can hold another team's rows must go
// through that team's handle instead. This reads every package of the module
// with types, so a call is caught whatever the variable holding the store is
// named, and refuses one that no entry vouches for.
func TestDefaultTeamTwinsAreCalledOnlyWhereTheDefaultTeamIsMeant(t *testing.T) {
	twins := defaultTeamTwinNames(t)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax | packages.NeedTypes | packages.NeedTypesInfo,
		Dir:  root,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) < 50 {
		t.Fatalf("loaded only %d packages; the check no longer reads the module", len(pkgs))
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		for _, e := range p.Errors {
			t.Fatalf("load %s: %v", p.PkgPath, e)
		}
		if p.PkgPath == modulePath+"/pkg/store" {
			continue
		}
		for _, f := range p.Syntax {
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				key := strings.TrimPrefix(p.PkgPath, modulePath+"/") + "." + funcKey(fn)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					sel, ok := n.(*ast.SelectorExpr)
					if !ok || !twins[sel.Sel.Name] {
						return true
					}
					obj, ok := p.TypesInfo.Uses[sel.Sel].(*types.Func)
					if !ok || !onStore(obj) {
						return true
					}
					seen[key] = true
					if _, vouched := defaultTeamCallers[key]; !vouched {
						t.Errorf("%s: %s calls Store.%s, which acts on the default team only; "+
							"go through the handle of the team that owns the row, or add %q to defaultTeamCallers "+
							"with the reason the default team is meant", p.Fset.Position(sel.Pos()), key, sel.Sel.Name, key)
					}
					return true
				})
			}
		}
	}
	for key, why := range defaultTeamCallers {
		if strings.TrimSpace(why) == "" {
			t.Errorf("defaultTeamCallers vouches for %q with no reason", key)
		}
		if !seen[key] {
			t.Errorf("defaultTeamCallers vouches for %q, which no longer calls a default-team twin; delete the entry", key)
		}
	}
}

func onStore(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil || fn.Pkg() == nil || fn.Pkg().Path() != modulePath+"/pkg/store" {
		return false
	}
	ptr, ok := sig.Recv().Type().(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := ptr.Elem().(*types.Named)
	return ok && named.Obj().Name() == "Store"
}

func defaultTeamTwinNames(t *testing.T) map[string]bool {
	t.Helper()
	twins := map[string]bool{}
	for _, f := range storeSourceFiles(t) {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv != nil && exprString(fn.Recv.List[0].Type) == "*Store" && delegatesToDefaultTeam(fn) {
				twins[fn.Name.Name] = true
			}
		}
	}
	return twins
}
