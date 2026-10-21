package main

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"
	"strings"

	"golang.org/x/tools/go/packages"
)

// These packages start repository-controlled code rather than host tooling.
func pipelinePackage(path string) bool {
	for _, name := range []string{"repos", "pipelineentry", "nodehost", "engine"} {
		root := modulePath + "/internal/" + name
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func pipelineEnvironmentFindings(pkg *packages.Package) []string {
	if !pipelinePackage(pkg.PkgPath) {
		return nil
	}
	var problems []string
	for _, file := range pkg.Syntax {
		directCalls := map[ast.Expr]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				directCalls[call.Fun] = true
			}
			return true
		})
		ast.Inspect(file, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			object := pkg.TypesInfo.Uses[id]
			if object == nil || object.Pkg() == nil || object.Pkg().Path() != modulePath+"/internal/procenv" {
				return true
			}
			switch object.Name() {
			case "HostCommand", "HostCommandContext", "AppendEnvironment":
				problems = append(problems, fmt.Sprintf("%s: pipeline environment: %s bypasses procenv.Build", pkg.Fset.Position(id.Pos()), object.Name()))
			case "Command", "CommandContext":
				called := directCalls[id]
				for expr := range directCalls {
					if selector, ok := expr.(*ast.SelectorExpr); ok && selector.Sel == id {
						called = true
					}
				}
				if !called {
					problems = append(problems, fmt.Sprintf("%s: pipeline environment: call %s directly so its environment can be checked", pkg.Fset.Position(id.Pos()), object.Name()))
				}
			}
			return true
		})
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok {
				fn = &ast.FuncDecl{Name: ast.NewIdent("<init>"), Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{List: []ast.Stmt{&ast.DeclStmt{Decl: declaration}}}}
			}
			if fn.Body == nil {
				continue
			}
			assignments := environmentAssignments(pkg, fn.Body)
			trustedParameter := func(expr ast.Expr) bool {
				id, ok := expr.(*ast.Ident)
				if !ok || len(assignments[pkg.TypesInfo.ObjectOf(id)]) != 0 {
					return false
				}
				// These forwarding seams receive the policy result from their caller.
				key := strings.TrimPrefix(pkg.PkgPath, modulePath+"/internal/") + ":" + fn.Name.Name
				want := map[string]string{
					"pipelineentry:Exec": "environment", "pipelineentry:NPMCommand": "environment",
					"nodehost:runCleanup": "env", "engine/sdkinstall:installTool": "environment",
				}[key]
				if id.Name != want {
					return false
				}
				for _, field := range fn.Type.Params.List {
					for _, parameter := range field.Names {
						if pkg.TypesInfo.ObjectOf(parameter) == pkg.TypesInfo.ObjectOf(id) {
							return true
						}
					}
				}
				return false
			}
			var fromPolicy func(ast.Expr, map[types.Object]bool) bool
			fromPolicy = func(expr ast.Expr, seen map[types.Object]bool) bool {
				switch value := expr.(type) {
				case *ast.ParenExpr:
					return fromPolicy(value.X, seen)
				case *ast.Ident:
					if trustedParameter(value) {
						return true
					}
					object := pkg.TypesInfo.ObjectOf(value)
					if object == nil || seen[object] || len(assignments[object]) == 0 {
						return false
					}
					seen[object] = true
					defer delete(seen, object)
					for _, rhs := range assignments[object] {
						if !fromPolicy(rhs, seen) {
							return false
						}
					}
					return true
				case *ast.SelectorExpr:
					// Protocol startup transports the already-projected environment.
					if pkg.PkgPath == modulePath+"/internal/nodehost" && fn.Name.Name == "StartProtocolSession" {
						if id, ok := value.X.(*ast.Ident); ok && id.Name == "cfg" && value.Sel.Name == "Environment" {
							return !assignsSelector(fn.Body, pkg, value)
						}
					}
				case *ast.CallExpr:
					object := calledFunction(pkg, value.Fun)
					if object != nil && object.Pkg() != nil {
						if object.Pkg().Path() == modulePath+"/internal/procenv" && object.Name() == "Build" {
							return true
						}
						if object.Pkg().Path() == modulePath+"/internal/nodehost" && object.Name() == "job" && fn.Name.Name == "CacheDirectory" {
							receiver := object.Type().(*types.Signature).Recv()
							if receiver != nil {
								named, ok := types.Unalias(receiver.Type()).(*types.Named)
								return ok && named.Obj().Name() == "SDKEnvironment"
							}
						}
					}
					if id, ok := value.Fun.(*ast.Ident); ok {
						if builtin, ok := pkg.TypesInfo.Uses[id].(*types.Builtin); ok && builtin.Name() == "append" && len(value.Args) > 1 {
							if fromPolicy(value.Args[0], seen) {
								// The engine's Go workspace setting is public execution configuration.
								for _, arg := range value.Args[1:] {
									v := pkg.TypesInfo.Types[arg].Value
									if v == nil || v.Kind() != constant.String || constant.StringVal(v) != "GOWORK=off" {
										return false
									}
								}
								return value.Ellipsis == 0
							}
							if len(value.Args) == 2 && value.Ellipsis != 0 {
								literal, ok := value.Args[0].(*ast.CompositeLit)
								return ok && len(literal.Elts) == 0 && fromPolicy(value.Args[1], seen)
							}
						}
					}
				}
				return false
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				object := calledFunction(pkg, call.Fun)
				if object == nil || object.Pkg() == nil {
					return true
				}
				index := -1
				switch object.Pkg().Path() {
				case modulePath + "/internal/procenv":
					if object.Name() == "Command" {
						index = 0
					} else if object.Name() == "CommandContext" {
						index = 1
					}
				case modulePath + "/internal/pipelineentry":
					if object.Name() == "Exec" {
						index = 0
					} else if object.Name() == "NPMCommand" {
						index = 1
					}
				}
				if index >= 0 && (len(call.Args) <= index || !fromPolicy(call.Args[index], map[types.Object]bool{})) {
					problems = append(problems, fmt.Sprintf("%s: pipeline environment: %s requires procenv.Build provenance", pkg.Fset.Position(call.Pos()), object.Name()))
				}
				return true
			})
		}
	}
	return problems
}

func calledFunction(pkg *packages.Package, expr ast.Expr) *types.Func {
	switch expr := expr.(type) {
	case *ast.Ident:
		fn, _ := pkg.TypesInfo.Uses[expr].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		fn, _ := pkg.TypesInfo.Uses[expr.Sel].(*types.Func)
		return fn
	}
	return nil
}

func environmentAssignments(pkg *packages.Package, body *ast.BlockStmt) map[types.Object][]ast.Expr {
	assignments := map[types.Object][]ast.Expr{}
	ast.Inspect(body, func(n ast.Node) bool {
		var lhs []ast.Expr
		var rhs []ast.Expr
		switch n := n.(type) {
		case *ast.AssignStmt:
			lhs, rhs = n.Lhs, n.Rhs
		case *ast.ValueSpec:
			for _, id := range n.Names {
				lhs = append(lhs, id)
			}
			rhs = n.Values
		case *ast.RangeStmt:
			lhs, rhs = []ast.Expr{n.Key, n.Value}, []ast.Expr{n.X}
		}
		for i, expr := range lhs {
			if id, ok := expr.(*ast.Ident); ok && len(rhs) != 0 {
				value := rhs[0]
				if len(lhs) == len(rhs) {
					value = rhs[i]
				}
				object := pkg.TypesInfo.ObjectOf(id)
				assignments[object] = append(assignments[object], value)
			}
		}
		return true
	})
	return assignments
}

func assignsSelector(body *ast.BlockStmt, pkg *packages.Package, target *ast.SelectorExpr) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if assignment, ok := n.(*ast.AssignStmt); ok {
			for _, lhs := range assignment.Lhs {
				if selector, ok := lhs.(*ast.SelectorExpr); ok && pkg.TypesInfo.ObjectOf(selector.Sel) == pkg.TypesInfo.ObjectOf(target.Sel) {
					found = true
				}
			}
		}
		return true
	})
	return found
}
