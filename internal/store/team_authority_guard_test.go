package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// safety: these read another team's authority only to filter it out, never to act for it.
var reviewedTeamAuthorityReads = map[string]string{
	"registeredAgentTx":    "lists a team's registered agents and skips any observed prefix of another team",
	"OwnerMachineClaimant": "answers whether an award's root machine belongs to this team, and a foreign root is a plain no",
}

var teamResolvers = []string{"resolveTeamAuthorityTx", "resolveTeamActorTx", "teamAuthorityChainTx", "teamPrincipal"}

// TestTeamScopedFunctionsResolveThroughTeam keeps the foreign-team refusal in the
// team resolvers: a *Tenant method, a function that takes a team, or one handed a
// value that carries its team may not resolve an authority or its chain directly
// and restate the team comparison by hand.
func TestTeamScopedFunctionsResolveThroughTeam(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, file)
	}
	carriers := teamCarriers(parsed)
	if !slices.Contains(carriers, "UnitClaim") {
		t.Fatalf("UnitClaim no longer carries its team as a field; carriers=%v", carriers)
	}
	checked, used := 0, map[string]bool{}
	for _, file := range parsed {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || slices.Contains(teamResolvers, fn.Name.Name) || !teamScopedFunc(fn, carriers) {
				continue
			}
			checked++
			calls := bareResolveAuthorityCalls(fn.Body)
			if _, ok := reviewedTeamAuthorityReads[fn.Name.Name]; ok && len(calls) > 0 {
				used[fn.Name.Name] = true
				continue
			}
			for _, call := range calls {
				t.Errorf("%s: %s is team-scoped; resolve its authority with resolveTeamAuthorityTx, resolveTeamActorTx or teamAuthorityChainTx", fset.Position(call), fn.Name.Name)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("guard judged only %d team-scoped functions; it is no longer reading this package", checked)
	}
	for name, why := range reviewedTeamAuthorityReads {
		if strings.TrimSpace(why) == "" || !used[name] {
			t.Errorf("reviewedTeamAuthorityReads entry %q is stale or has no reason; delete it", name)
		}
	}
}

func TestTeamAuthorityGuardCatchesBareResolve(t *testing.T) {
	src := `package store
type claimLike struct{ Team Team }
func (t *Tenant) write(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }
func helperTx(ctx context.Context, tx *storeTx, team Team, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }
func actorTx(ctx context.Context, tx *storeTx, team Team, actor runstate.Actor) { resolveActorTx(ctx, tx, actor) }
func chainTx(ctx context.Context, tx *storeTx, team Team, ref capability.AuthorityRef) { authorityChainTx(ctx, tx, ref, true) }
func embeddedTx(ctx context.Context, tx *storeTx, claim claimLike, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }
func (o *Operator) deployment(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	carriers := teamCarriers([]*ast.File{file})
	var flagged []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && teamScopedFunc(fn, carriers) && len(bareResolveAuthorityCalls(fn.Body)) > 0 {
			flagged = append(flagged, fn.Name.Name)
		}
	}
	if strings.Join(flagged, ",") != "write,helperTx,actorTx,chainTx,embeddedTx" {
		t.Fatalf("guard flagged %v, want every team-scoped resolver use and no deployment one", flagged)
	}
}

// safety: a struct with a Team field hands its function a team as surely as a team parameter does.
func teamCarriers(files []*ast.File) []string {
	var out []string
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			if st, ok := spec.Type.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					id, ok := field.Type.(*ast.Ident)
					if ok && id.Name == "Team" && slices.ContainsFunc(field.Names, func(n *ast.Ident) bool { return n.Name == "Team" }) {
						out = append(out, spec.Name.Name)
					}
				}
			}
			return true
		})
	}
	return out
}

func teamScopedFunc(fn *ast.FuncDecl, carriers []string) bool {
	if fn.Recv != nil {
		if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok && (id.Name == "Tenant" || id.Name == "Operator") {
				// safety: Operator methods are deployment-wide by construction; a Tenant method is always scoped.
				return id.Name == "Tenant"
			}
		}
	}
	for _, field := range fn.Type.Params.List {
		typ := field.Type
		if star, ok := typ.(*ast.StarExpr); ok {
			typ = star.X
		}
		if id, ok := typ.(*ast.Ident); ok && (id.Name == "Team" || slices.Contains(carriers, id.Name)) {
			return true
		}
		for _, name := range field.Names {
			if name.Name == "team" {
				return true
			}
		}
	}
	return false
}

func bareResolveAuthorityCalls(body *ast.BlockStmt) []token.Pos {
	var calls []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "ResolveAuthority" {
				calls = append(calls, call.Pos())
			}
			if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "resolveActorTx" || id.Name == "authorityChainTx") {
				calls = append(calls, call.Pos())
			}
		}
		return true
	})
	return calls
}
