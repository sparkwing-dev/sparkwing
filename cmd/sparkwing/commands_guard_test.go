package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"
)

func TestAllCommandsAreRegistered(t *testing.T) {
	declared := commandVarsInSource(t, "help_registry.go")
	registered := registeredCommandPaths(t)

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

func TestCommandsMarkdownEndsWithOneNewline(t *testing.T) {
	got := renderCommandsMarkdown([]CommandJSON{{
		Path:     "sparkwing example",
		Synopsis: "Example command",
		Examples: []ExampleJSON{{Command: "sparkwing example"}},
	}})
	if !strings.HasSuffix(got, "\n") || strings.HasSuffix(got, "\n\n") {
		t.Fatalf("generated Markdown must end with exactly one newline, got suffix %q", got[len(got)-min(4, len(got)):])
	}
}

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

func registeredCommandPaths(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "help_registry.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse help_registry.go: %v", err)
	}
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

// hack: these dispatchers never pass their Command to handleParentHelp or
// PrintHelp, so the walk cannot infer the group their cases belong to.
var dispatchersWithoutHelp = map[string]string{
	"runQueue":    "cmdQueue",
	"runVersion":  "cmdVersion",
	"runExamples": "cmdExamples",
	"runWingd":    "cmdWingd",
}

func TestEveryDispatchedVerbIsRegistered(t *testing.T) {
	varPaths := registryVarPaths(t)
	registered := map[string]*Command{}
	for _, c := range allCommands {
		registered[c.Path] = c
	}
	dispatched := dispatchedVerbs(t, varPaths)
	if len(dispatched["sparkwing"]) == 0 {
		t.Fatal("found no top-level dispatch cases, so this check proves nothing")
	}

	for group, verbs := range dispatched {
		for verb := range verbs {
			if registered[group+" "+verb] == nil {
				t.Errorf("%s dispatches %q, but %q is not a registered command; register it (Hidden when internal) or delete the case",
					group, verb, group+" "+verb)
			}
		}
	}
	for path := range registered {
		if path == "sparkwing" || len(childCommands(path)) > 0 {
			continue
		}
		i := strings.LastIndex(path, " ")
		if !dispatched[path[:i]][path[i+1:]] {
			t.Errorf("%s is registered, but no dispatcher for %q has a case %q", path, path[:i], path[i+1:])
		}
	}
}

func dispatchedVerbs(t *testing.T, varPaths map[string]string) map[string]map[string]bool {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	consts := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files[name] = f
		for _, decl := range f.Decls {
			if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.CONST {
				for _, spec := range gen.Specs {
					vs := spec.(*ast.ValueSpec)
					for i, n := range vs.Names {
						if i < len(vs.Values) {
							if v, ok := stringLiteral(vs.Values[i]); ok {
								consts[n.Name] = v
							}
						}
					}
				}
			}
		}
	}
	literal := func(e ast.Expr) (string, bool) {
		if id, ok := e.(*ast.Ident); ok {
			v, ok := consts[id.Name]
			return v, ok
		}
		return stringLiteral(e)
	}
	out := map[string]map[string]bool{}
	for name, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var verbs []string
			group := dispatchersWithoutHelp[fn.Name.Name]
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					if id, ok := n.Fun.(*ast.Ident); ok && (id.Name == "handleParentHelp" || id.Name == "PrintHelp") && len(n.Args) > 0 {
						if arg, ok := n.Args[0].(*ast.Ident); ok && group == "" && varPaths[arg.Name] != "" {
							group = arg.Name
						}
					}
				case *ast.SwitchStmt:
					if isArgsZero(n.Tag) {
						for _, stmt := range n.Body.List {
							for _, e := range stmt.(*ast.CaseClause).List {
								if v, ok := literal(e); ok {
									verbs = append(verbs, v)
								}
							}
						}
					}
				case *ast.BinaryExpr:
					if n.Op == token.EQL && isArgsZero(n.X) {
						if v, ok := literal(n.Y); ok {
							verbs = append(verbs, v)
						}
					}
				}
				return true
			})
			var real []string
			for _, v := range verbs {
				if v != "help" && !strings.HasPrefix(v, "-") {
					real = append(real, v)
				}
			}
			if len(real) == 0 {
				continue
			}
			if group == "" {
				t.Errorf("%s: %s dispatches %v but names no Command; add it to dispatchersWithoutHelp", name, fn.Name.Name, real)
				continue
			}
			path := varPaths[group]
			if out[path] == nil {
				out[path] = map[string]bool{}
			}
			for _, v := range real {
				out[path][v] = true
			}
		}
	}
	return out
}

func isArgsZero(e ast.Expr) bool {
	idx, ok := e.(*ast.IndexExpr)
	if !ok {
		return false
	}
	id, ok := idx.X.(*ast.Ident)
	lit, isLit := idx.Index.(*ast.BasicLit)
	return ok && id.Name == "args" && isLit && lit.Value == "0"
}
