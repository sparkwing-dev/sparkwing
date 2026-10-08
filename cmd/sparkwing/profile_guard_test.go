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
		t.Error("sparkwing run must execute locally and accept no --profile")
	}
}

// A local run selects no profile and reads no controller secrets. Non-test code
// in cmd/sparkwing and internal/orchestrator may name the profile handoff
// variables only to drop them, and may build a controller secret source only
// where a controller dispatched the run.
func TestALocalRunReachesNoProfileOrControllerSecrets(t *testing.T) {
	handoff := map[string]bool{"ProfileEnv": true, "SecretsProfileEnv": true}
	controllerSecrets := map[string]bool{"controllerSecretSource": true, "GetSecretForRun": true}
	removed := map[string]bool{
		"projectDefaultProfile": true, "resolveDefaultProfile": true, "SetDefaultProfile": true,
		"resolveActiveProfile": true, "applySecretsProfileOverride": true, "remoteSecretSource": true,
		"selectSecretResolver": true, "ApplyProfileBackends": true, "newMirrorStateBackend": true,
	}
	type rules struct{ handoff, controllerSecrets map[string]bool }
	dirs := map[string]rules{
		".": {handoff: map[string]bool{"stripProfileHandoff": true, "renderHookScript": true}},
		"../../internal/orchestrator": {
			handoff:           map[string]bool{"submissionExecutionEnvironment": true},
			controllerSecrets: map[string]bool{"controllerSecretSource": true, "ExecuteClaimedTrigger": true, "RunNodeOnce": true},
		},
	}
	checked := 0
	for dir, allow := range dirs {
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
				owner := ""
				if fn, ok := decl.(*ast.FuncDecl); ok {
					owner = fn.Name.Name
				}
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
	}
	if checked == 0 {
		t.Fatal("parsed no source, so this check proves nothing")
	}
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
