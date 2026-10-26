package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestTeamScopedFunctionsResolveThroughTeam keeps the foreign-team refusal in
// resolveTeamAuthorityTx: a *Tenant method, or a function that takes a team, may
// not call ResolveAuthority directly and restate the team comparison by hand.
func TestTeamScopedFunctionsResolveThroughTeam(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == "resolveTeamAuthorityTx" || fn.Name.Name == "resolveTeamActorTx" || !teamScopedFunc(fn) {
				continue
			}
			checked++
			for _, call := range bareResolveAuthorityCalls(fn.Body) {
				t.Errorf("%s: %s is team-scoped; resolve its authority with resolveTeamAuthorityTx or resolveTeamActorTx", fset.Position(call), fn.Name.Name)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("guard judged only %d team-scoped functions; it is no longer reading this package", checked)
	}
}

func TestTeamAuthorityGuardCatchesBareResolve(t *testing.T) {
	src := `package store
func (t *Tenant) write(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }
func helperTx(ctx context.Context, tx *storeTx, team Team, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }
func actorTx(ctx context.Context, tx *storeTx, team Team, actor runstate.Actor) { resolveActorTx(ctx, tx, actor) }
func (o *Operator) deployment(ctx context.Context, tx *storeTx, ref capability.AuthorityRef) { tx.ResolveAuthority(ctx, ref) }`
	file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var flagged []string
	for _, decl := range file.Decls {
		fn := decl.(*ast.FuncDecl)
		if teamScopedFunc(fn) && len(bareResolveAuthorityCalls(fn.Body)) > 0 {
			flagged = append(flagged, fn.Name.Name)
		}
	}
	if strings.Join(flagged, ",") != "write,helperTx,actorTx" {
		t.Fatalf("guard flagged %v, want the Tenant method and the team helper only", flagged)
	}
}

func teamScopedFunc(fn *ast.FuncDecl) bool {
	if fn.Recv != nil {
		if star, ok := fn.Recv.List[0].Type.(*ast.StarExpr); ok {
			if id, ok := star.X.(*ast.Ident); ok && id.Name == "Tenant" {
				return true
			}
		}
	}
	for _, field := range fn.Type.Params.List {
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "Team" {
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
			if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "resolveActorTx" {
				calls = append(calls, call.Pos())
			}
		}
		return true
	})
	return calls
}
