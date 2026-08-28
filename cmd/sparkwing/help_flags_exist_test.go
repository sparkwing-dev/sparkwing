package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestEveryRegistryFlagIsRegisteredInSource(t *testing.T) {
	varPaths := registryVarPaths(t)
	registered := flagsRegisteredPerCommand(t, varPaths)
	if len(registered) == 0 {
		t.Fatal("found no flag registrations at all, so this check proves nothing")
	}

	checked := 0
	for _, cmd := range allCommands {
		if len(cmd.Flags) == 0 {
			continue
		}
		have, ok := registered[cmd.Path]
		if !ok {

			continue
		}
		checked++
		for _, f := range cmd.Flags {
			if f.Type != "" {
				continue
			}
			if slices.Contains(have, f.Name) {
				continue
			}
			t.Errorf("%s: the registry advertises --%s, which no FlagSet for that command registers; "+
				"either register it or drop the claim (help, completion, and the CLI reference all read this)",
				cmd.Path, f.Name)
		}
	}
	if checked == 0 {
		t.Fatal("matched no command to its FlagSet, so this check proves nothing")
	}
}

func TestProfileFlagRegistrationsDoNotAdvertiseCurrentDefault(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/sparkwing: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "String" && sel.Sel.Name != "StringP") {
				return true
			}
			flagName, ok := stringLiteral(call.Args[0])
			if !ok || flagName != "profile" {
				return true
			}
			description, ok := stringLiteral(call.Args[len(call.Args)-1])
			if !ok {
				t.Errorf("%s registers --profile with a description this check cannot inspect", fset.Position(call.Pos()))
				return true
			}
			if strings.Contains(strings.ToLower(description), "current default") {
				t.Errorf("%s advertises a nonexistent current default profile in %q", fset.Position(call.Pos()), description)
			}
			return true
		})
	}
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	return value, err == nil
}

func flagsRegisteredPerCommand(t *testing.T, varPaths map[string]string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read cmd/sparkwing: %v", err)
	}

	var funcs []*ast.FuncDecl
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs = append(funcs, fn)
			}
		}
	}

	helpers := helperFlagSets(funcs)
	byPath := map[string][]string{}
	for _, fn := range funcs {
		for cmdIdent, fsVar := range flagSetsIn(fn) {
			path := varPaths[cmdIdent]
			if path == "" {
				continue
			}
			byPath[path] = append(byPath[path], flagNamesOn(fn, fsVar, helpers)...)
		}
	}
	return byPath
}

func helperFlagSets(funcs []*ast.FuncDecl) map[string][]string {
	out := map[string][]string{}
	for range 3 {
		for _, fn := range funcs {
			param := flagSetParam(fn)
			if param == "" {
				continue
			}
			out[fn.Name.Name] = flagNamesOn(fn, param, out)
		}
	}
	return out
}

func flagSetParam(fn *ast.FuncDecl) string {
	if fn.Type.Params == nil {
		return ""
	}
	for _, field := range fn.Type.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok || !isSelector(star.X, "flag", "FlagSet") || len(field.Names) == 0 {
			continue
		}
		return field.Names[0].Name
	}
	return ""
}

func flagSetsIn(fn *ast.FuncDecl) map[string]string {
	out := map[string]string{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		lhs, ok := assign.Lhs[0].(*ast.Ident)
		if !ok {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok || !isSelector(call.Fun, "flag", "NewFlagSet") || len(call.Args) == 0 {
			return true
		}
		sel, ok := call.Args[0].(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Path" {
			return true
		}
		cmdIdent, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		out[cmdIdent.Name] = lhs.Name
		return true
	})
	return out
}

func flagNamesOn(fn *ast.FuncDecl, fsVar string, helpers map[string][]string) []string {
	var names []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || recv.Name != fsVar {
			return true
		}
		if !strings.HasPrefix(sel.Sel.Name, "String") && !strings.HasPrefix(sel.Sel.Name, "Bool") &&
			!strings.HasPrefix(sel.Sel.Name, "Int") && !strings.HasPrefix(sel.Sel.Name, "Duration") &&
			!strings.HasPrefix(sel.Sel.Name, "Float") && !strings.HasPrefix(sel.Sel.Name, "Var") {
			return true
		}
		for _, arg := range call.Args {
			lit, ok := arg.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			if v, err := strconv.Unquote(lit.Value); err == nil && v != "" {
				names = append(names, v)
			}
			break
		}
		return true
	})

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		passes := false
		for _, arg := range call.Args {
			if a, ok := arg.(*ast.Ident); ok && a.Name == fsVar {
				passes = true
				break
			}
		}
		if !passes {
			return true
		}
		names = append(names, helpers[id.Name]...)

		for _, arg := range call.Args {
			if lit, ok := arg.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if v, err := strconv.Unquote(lit.Value); err == nil && v != "" {
					names = append(names, v)
				}
				break
			}
		}
		return true
	})
	return names
}

func isSelector(e ast.Expr, pkg, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

func registryVarPaths(t *testing.T) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "help_registry.go", nil, 0)
	if err != nil {
		t.Fatalf("parse help_registry.go: %v", err)
	}
	out := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.CompositeLit)
		if !ok {
			return true
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
				if v, err := strconv.Unquote(bl.Value); err == nil {
					out[spec.Names[0].Name] = v
				}
			}
		}
		return true
	})
	if len(out) == 0 {
		t.Fatal("help_registry.go declared no command paths, so this check proves nothing")
	}
	return out
}
