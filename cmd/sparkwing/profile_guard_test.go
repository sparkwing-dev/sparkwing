package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestEveryControllerVerbAcceptsProfile(t *testing.T) {
	// safety: it prints a profile's config and contacts nothing.
	readsConfigOnly := map[string]bool{"sparkwing configure profiles show": true}
	marked := 0
	for _, cmd := range allCommands {
		accepts := cmd.declaredFlags()["profile"]
		if cmd.ReachesController {
			marked++
			if !accepts {
				t.Errorf("%s reaches a controller but does not accept --profile, so nothing on the command line would name its target", cmd.Path)
			}
			continue
		}
		if accepts && !readsConfigOnly[cmd.Path] {
			t.Errorf("%s accepts --profile but is not marked ReachesController", cmd.Path)
		}
	}
	if marked == 0 {
		t.Fatal("no command is marked ReachesController, so this check proves nothing")
	}
	if cmdRun.ReachesController || cmdRun.declaredFlags()["profile"] {
		t.Error("sparkwing run executes on this machine; it neither reaches a controller nor declares --profile")
	}
}

// A run selects no profile. Non-test code in cmd/sparkwing and
// internal/orchestrator may name the handoff variables only where they are
// dropped before a pipeline process starts, and may build a controller secret
// source only where a controller dispatched the run.
func TestALocalRunReachesNoProfileOrControllerSecrets(t *testing.T) {
	checked := 0
	for dir, allow := range profileGuardDirs {
		checked += checkProfileGuard(t, dir, allow)
	}
	if checked == 0 {
		t.Fatal("parsed no source, so this check proves nothing")
	}
}

func TestProfileGuardRefusesALocalPodSecretResolver(t *testing.T) {
	dir := t.TempDir()
	src := `package orchestrator

func localSecretsFor(runID string) any {
	return podSecrets{runID: runID}
}
`
	if err := os.WriteFile(filepath.Join(dir, "local_secrets.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	var failed bool
	checkProfileGuard(&guardRecorder{T: t, failed: &failed}, dir, profileGuardDirs["../../internal/orchestrator"])
	if !failed {
		t.Fatal("a local secrets source built from the pod resolver passed the guard")
	}
}

type profileGuardRules struct{ handoff, controllerSecrets map[string]bool }

var profileGuardDirs = map[string]profileGuardRules{
	".": {handoff: map[string]bool{"dropProfileEnv": true, "renderHookScript": true}},
	"../../internal/orchestrator": {
		handoff: map[string]bool{"refuseProfileHandoff": true},
		controllerSecrets: map[string]bool{
			"controllerSecretSource": true, "ExecuteClaimedTrigger": true,
		},
	},
}

type guardReporter interface {
	Helper()
	Fatal(...any)
	Errorf(string, ...any)
}

// hack: the negative case needs the guard's findings recorded, not failed.
type guardRecorder struct {
	*testing.T
	failed *bool
}

func (r *guardRecorder) Errorf(string, ...any) { *r.failed = true }

func checkProfileGuard(t guardReporter, dir string, allow profileGuardRules) int {
	t.Helper()
	handoff := map[string]bool{"ProfileEnv": true, "SecretsProfileEnv": true}
	controllerSecrets := map[string]bool{"controllerSecretSource": true, "GetSecretForRun": true, "podSecrets": true}
	removed := map[string]bool{
		"projectDefaultProfile": true, "resolveDefaultProfile": true, "SetDefaultProfile": true,
		"applySecretsProfileOverride": true, "remoteSecretSource": true, "handoffSecretsProfile": true,
		"resolveActiveProfile": true, "RemoteStateProfile": true, "handoffProfiles": true, "selectSecretResolver": true,
	}
	checked := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly|parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range file.Imports {
			if strings.HasSuffix(strings.Trim(imp.Path.Value, `"`), "/internal/secretsource") {
				t.Errorf("%s imports secretsource, which resolves a profile's remote secrets surface", fset.Position(imp.Pos()))
			}
		}
		file, err = parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, decl := range file.Decls {
			if declaresHandoff(decl, handoff) {
				continue
			}
			owner := declOwner(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.BasicLit:
					if v, err := strconv.Unquote(n.Value); err == nil && strings.Contains(v, "SPARKWING_") && strings.Contains(v, "PROFILE") &&
						!strings.Contains(v, "SPARKWING_PROFILES") {
						t.Errorf("%s: names %q; drop it through stripProfileHandoff", fset.Position(n.Pos()), v)
					}
				case *ast.Ident:
					if removed[n.Name] {
						t.Errorf("%s: %s selects a profile or secrets source the command line did not name", fset.Position(n.Pos()), n.Name)
					}
					if handoff[n.Name] && !allow.handoff[owner] {
						t.Errorf("%s: %s uses %s; only the code that drops it may", fset.Position(n.Pos()), owner, n.Name)
					}
					if controllerSecrets[n.Name] && !allow.controllerSecrets[owner] {
						t.Errorf("%s: %s reads controller secrets; only a run a controller dispatched may", fset.Position(n.Pos()), owner)
					}
				}
				return true
			})
		}
	}
	return checked
}

// safety: a type declaration is owned by its type, so declaring podSecrets is not read as a local caller.
func declOwner(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil {
			return receiverName(d.Recv.List[0].Type) + "." + d.Name.Name
		}
		return d.Name.Name
	case *ast.GenDecl:
		if d.Tok == token.TYPE && len(d.Specs) == 1 {
			return d.Specs[0].(*ast.TypeSpec).Name.Name
		}
	}
	return ""
}

func declaresHandoff(decl ast.Decl, handoff map[string]bool) bool {
	gen, ok := decl.(*ast.GenDecl)
	if !ok || gen.Tok != token.CONST {
		return false
	}
	for _, spec := range gen.Specs {
		for _, name := range spec.(*ast.ValueSpec).Names {
			if !handoff[name.Name] {
				return false
			}
		}
	}
	return true
}

func receiverName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		expr = star.X
	}
	if id, ok := expr.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}
