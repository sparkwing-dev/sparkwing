package main

// Guardrail: every Command declared in help_registry.go MUST be
// listed in allCommands (commands.go). Without this, `sparkwing
// commands` -- the agent-facing self-discovery probe -- silently
// skips verbs that exist in the binary, and `--help --json` on
// those verbs only renders prose. Both failure modes are invisible
// to the implementer.
//
// Mechanism: parse help_registry.go's source AST, find every
// top-level `var cmdX = Command{...}` declaration, then verify each
// is present in the in-memory allCommands slice. Source-driven so
// adding a new var in the .go file without updating allCommands
// fails CI -- no double-bookkeeping risk.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"testing"
)

func TestAllCommandsAreRegistered(t *testing.T) {
	declared := commandVarsInSource(t, "help_registry.go")
	registered := registeredCommandPaths()

	missing := map[string]bool{}
	for _, name := range declared {
		if !registered[name] {
			missing[name] = true
		}
	}
	if len(missing) > 0 {
		var names []string
		for n := range missing {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Fatalf("commands declared in help_registry.go but missing from allCommands in commands.go:\n  %s\n\n"+
			"Add them to the allCommands slice so `sparkwing commands` and `--help --json` see them.",
			strings.Join(names, "\n  "))
	}

	// Reverse check: a name in allCommands that isn't in the source
	// means a stale entry left behind after a Command was renamed.
	declaredSet := map[string]bool{}
	for _, n := range declared {
		declaredSet[n] = true
	}
	var orphans []string
	for n := range registered {
		if !declaredSet[n] {
			orphans = append(orphans, n)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Fatalf("allCommands references commands that don't exist in help_registry.go:\n  %s\n\n"+
			"Remove these entries from allCommands -- they're stale.",
			strings.Join(orphans, "\n  "))
	}
}

// commandVarsInSource parses path and returns every top-level
// identifier matching `cmd[A-Z]...` whose declaration is `=
// Command{...}`. Robust to formatter/comment changes since we walk
// the AST instead of regex-matching the file body.
func commandVarsInSource(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "cmd") {
					continue
				}
				if len(name.Name) < 4 || name.Name[3] < 'A' || name.Name[3] > 'Z' {
					continue
				}
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					continue
				}
				ident, ok := lit.Type.(*ast.Ident)
				if !ok || ident.Name != "Command" {
					continue
				}
				names = append(names, name.Name)
			}
		}
	}
	return names
}

// registeredCommandPaths returns the set of var-name -> Path
// mappings derived from allCommands. Var names aren't directly
// available at runtime, so we round-trip through Path: declared in
// source as `var cmdFoo = Command{Path: "sparkwing foo"}`, and
// matched by walking the source AST. Both sides eventually compare
// var-names; the registered set returns var names by deriving them
// from the Path field via the same helper used at parse time.
func registeredCommandPaths() map[string]bool {
	// Parse the same file once more to map var-name -> Path so the
	// guard compares like-for-like. Not the prettiest but it keeps
	// the test self-contained.
	fset := token.NewFileSet()
	f, _ := parser.ParseFile(fset, "help_registry.go", nil, parser.SkipObjectResolution)
	pathByName := map[string]string{}
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "Path" {
						continue
					}
					if bl, ok := kv.Value.(*ast.BasicLit); ok && bl.Kind == token.STRING {
						pathByName[name.Name] = strings.Trim(bl.Value, `"`)
					}
				}
			}
		}
	}
	registered := map[string]bool{}
	for _, c := range allCommands {
		for name, p := range pathByName {
			if p == c.Path {
				registered[name] = true
				break
			}
		}
	}
	return registered
}
