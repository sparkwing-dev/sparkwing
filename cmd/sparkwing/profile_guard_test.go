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
}

// The pipeline process reads the profile handoff variables, so they may be
// named only where the CLI writes them from its own flags, where a child's
// environment drops them, and in the accessors the orchestrator reads them
// through.
func TestOnlyTheFlagSelectsAProfile(t *testing.T) {
	handoff := map[string]bool{"ProfileEnv": true, "SecretsProfileEnv": true}
	removed := map[string]bool{"projectDefaultProfile": true, "resolveDefaultProfile": true, "SetDefaultProfile": true}
	allowed := map[string]map[string]bool{
		".":                           {"handoffProfiles": true, "renderHookScript": true},
		"../../internal/orchestrator": {"handoffProfile": true, "handoffSecretsProfile": true, "submissionExecutionEnvironment": true},
	}
	checked := 0
	for dir, funcs := range allowed {
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
			file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
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
							t.Errorf("%s: names %q; read the handoff through orchestrator.ProfileEnv in a reviewed function", fset.Position(n.Pos()), v)
						}
					case *ast.Ident:
						if removed[n.Name] {
							t.Errorf("%s: %s resolves a profile the command line did not name", fset.Position(n.Pos()), n.Name)
						}
						if handoff[n.Name] && !funcs[owner] {
							t.Errorf("%s: %s uses %s; only the flag handoff may", fset.Position(n.Pos()), owner, n.Name)
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
