package procenv

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func TestPipelineSpawnsUseSharedEnvironmentPolicy(t *testing.T) {
	delegates := map[string]string{
		"nodehost/protocol_session.go:StartProtocolSession": "append([]string{}, cfg.Environment...)",
		"nodehost/cleanups.go:runCleanup":                   "env",
		"nodehost/cache_directory.go:CacheDirectory":        "env",
		"engine/sdkinstall/install.go:installTool":          "environment",
		"engine/upgrade.go:nativeCommand":                   "append(environment, \"GOWORK=off\")",
	}
	factories := map[string]bool{"pipelineentry/pipelineentry.go:Exec": true, "pipelineentry/npm.go:NPMCommand": true}
	root := filepath.Join("..")
	for _, directory := range []string{"repos", "pipelineentry", "nodehost", "engine"} {
		err := filepath.WalkDir(filepath.Join(root, directory), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() && entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			for _, declaration := range file.Decls {
				fn, ok := declaration.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				relative, err := filepath.Rel(root, path)
				if err != nil {
					return err
				}
				key := filepath.ToSlash(relative) + ":" + fn.Name.Name
				assignments := map[string]ast.Expr{}
				var environments []ast.Expr
				spawn := false
				ast.Inspect(fn.Body, func(node ast.Node) bool {
					if call, ok := node.(*ast.CallExpr); ok {
						if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
							spawn = spawn || selector.Sel.Name == "Exec" || selector.Sel.Name == "NPMCommand" || expressionText(selector.X) == "exec" && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext")
						}
					}
					assignment, ok := node.(*ast.AssignStmt)
					if !ok {
						return true
					}
					for index, lhs := range assignment.Lhs {
						rhs := assignment.Rhs[0]
						if len(assignment.Rhs) == len(assignment.Lhs) {
							rhs = assignment.Rhs[index]
						}
						assignments[expressionText(lhs)] = rhs
						if selector, ok := lhs.(*ast.SelectorExpr); ok && selector.Sel.Name == "Env" {
							environments = append(environments, rhs)
						}
					}
					return true
				})
				if spawn && len(environments) == 0 && !factories[key] {
					t.Errorf("%s spawns without an explicit shared environment", key)
				}
				for _, env := range environments {
					if expressionText(env) == delegates[key] && validDelegate(key, assignments) {
						continue
					}
					if !fromSharedBuild(env, assignments, map[string]bool{}) {
						t.Errorf("%s constructs its spawn environment outside procenv.Build: %s", key, expressionText(env))
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func validDelegate(key string, assignments map[string]ast.Expr) bool {
	switch key {
	case "nodehost/cleanups.go:runCleanup":
		return assignments["env"] == nil
	case "engine/sdkinstall/install.go:installTool":
		return assignments["environment"] == nil
	case "nodehost/protocol_session.go:StartProtocolSession":
		return assignments["cfg.Environment"] == nil
	case "nodehost/cache_directory.go:CacheDirectory":
		call, ok := assignments["env"].(*ast.CallExpr)
		return ok && expressionText(call.Fun) == "e.job"
	case "engine/upgrade.go:nativeCommand":
		return fromSharedBuild(assignments["environment"], assignments, map[string]bool{})
	}
	return false
}

func fromSharedBuild(expr ast.Expr, assignments map[string]ast.Expr, seen map[string]bool) bool {
	switch value := expr.(type) {
	case *ast.Ident:
		if seen[value.Name] {
			return false
		}
		seen[value.Name] = true
		return fromSharedBuild(assignments[value.Name], assignments, seen)
	case *ast.CallExpr:
		if expressionText(value.Fun) == "procenv.Build" {
			return true
		}
	}
	return false
}

func expressionText(node ast.Node) string {
	if node == nil {
		return ""
	}
	var output bytes.Buffer
	if err := format.Node(&output, token.NewFileSet(), node); err != nil {
		return ""
	}
	return output.String()
}
