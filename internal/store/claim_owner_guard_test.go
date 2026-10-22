package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestClaimOwnerHasNoLauncherScanOrAwardBypass(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve claim module source")
	}
	directory := filepath.Dir(source)
	files, err := filepath.Glob(filepath.Join(directory, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"launcherSweep", "launcher_sweep/", "launcher_claim_team", "queuedLauncherUnitTx", "SAVEPOINT launcher_award", "lockRankedUnitTx", `"ranking:"`, "rankingHoldTx", "unitPlacementTx", "placementRunnerTx"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("%s restores %s", path, forbidden)
			}
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, "unit_claim.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claimUnit", "claimUnitTx"} {
		checked := false
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != name {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				ident, ok := call.Fun.(*ast.Ident)
				if ok && ident.Name == "queuedUnitTx" {
					checked = true
				}
				return true
			})
		}
		if !checked {
			t.Fatalf("%s bypasses SQL eligibility", name)
		}
	}
	owner, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, "owner_placement.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range owner.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ownerDemandEligibleTx" {
			continue
		}
		shared := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "claimQueryTx" {
					shared = true
				}
			}
			return true
		})
		if !shared {
			t.Fatal("owner execution projection bypasses SQL eligibility")
		}
	}
}

// Fragments correlate team keys with the final authenticated work/claimant pair.
// They cannot execute SQL or accept database handles.
var reviewedClaimSQLFragments = map[string]string{
	"(*claimSQL).requirementRoutes": "shared authenticated label routes restrict both asks and indexed selection before the final eligibility predicate",
	"(*claimSQL).sourceScope":       "the globally unique run ID supplies the team index key; the shared predicate still authorizes its claimant",
	"(*claimSQL).predicate":         "eligibility correlates every tenant row with the work team's authenticated claimant facts",
	"(*claimSQL).source":            "the final predicate restricts the union of ready work to authorized teams",
	"(*claimSQL).query":             "the generated query joins tenant-keyed source rows and applies authenticated scope before selection or asks",
	"(*claimSQL).placementCTEs":     "ranking asks correlate with the already eligible tenant/work pair",
}

func TestClaimSQLFragmentsCannotExecuteOrCarryDatabaseHandles(t *testing.T) {
	files := []string{"claim_query.go", "claim_search.go"}
	check := func(n ast.Node) {
		ast.Inspect(n, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.Ident:
				switch v.Name {
				case "Store", "storeTx", "Operator", "Tenant", "sql", "rowCursor":
					t.Errorf("claim SQL builder refers to database handle %s", v.Name)
				}
			case *ast.CallExpr:
				if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
					switch sel.Sel.Name {
					case "Exec", "ExecContext", "Query", "QueryContext", "QueryRow", "QueryRowContext", "Begin", "BeginTx", "Commit", "Rollback", "Prepare", "PrepareContext":
						t.Errorf("claim SQL builder executes %s", sel.Sel.Name)
					}
				}
			}
			return true
		})
	}
	for _, path := range files {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			switch v := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range v.Specs {
					if typ, ok := spec.(*ast.TypeSpec); ok && typ.Name.Name == "claimSQL" {
						check(typ.Type)
					}
				}
			case *ast.FuncDecl:
				if v.Recv != nil && len(v.Recv.List) == 1 {
					star, ok := v.Recv.List[0].Type.(*ast.StarExpr)
					if ok {
						if typ, ok := star.X.(*ast.Ident); ok && typ.Name == "claimSQL" {
							check(v.Type)
							check(v.Body)
						}
					}
				}
			}
		}
	}
}
