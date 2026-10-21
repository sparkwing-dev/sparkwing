package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

func spawnFindings(repo string) ([]string, error) {
	var problems []string
	uncovered, err := loadSpawnPackages(repo, func(pkg *packages.Package) {
		problems = append(problems, packageSpawnFindings(pkg)...)
	})
	if err != nil {
		return nil, err
	}
	problems = append(problems, uncovered...)
	problems = dedupe(problems)
	sort.Strings(problems)
	return problems, nil
}

func packageSpawnFindings(pkg *packages.Package) []string {
	if pkg.PkgPath == modulePath+"/internal/procenv" {
		return nil
	}
	var problems []string
	report := func(n ast.Node, why string) {
		problems = append(problems, fmt.Sprintf("%s: spawn boundary: %s; use procenv with an explicit environment", pkg.Fset.Position(n.Pos()), why))
	}
	for _, file := range pkg.Syntax {
		if strings.HasSuffix(filepath.Base(pkg.Fset.Position(file.Pos()).Filename), "_test.go") {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				if instance, ok := pkg.TypesInfo.Instances[node]; ok {
					for i := 0; i < instance.TypeArgs.Len(); i++ {
						if rawCommandType(instance.TypeArgs.At(i)) {
							report(node, "exec.Cmd generic value construction")
						}
					}
				}
				object := pkg.TypesInfo.Uses[node]
				if object == nil || object.Pkg() == nil {
					break
				}
				path, name := object.Pkg().Path(), object.Name()
				if pipelinePackage(pkg.PkgPath) && path == modulePath+"/internal/procenv" {
					switch name {
					case "HostCommand", "HostCommandContext", "ControlCommand", "ControlCommandContext", "ConfigureWindowsBash", "Replace":
						report(node, "pipeline processes require procenv.Command or CommandContext with procenv.Env, not "+name)
					}
				}
				forbidden := path == "os/exec" && (name == "Command" || name == "CommandContext") || path == "os" && name == "StartProcess" || (path == "syscall" || path == "golang.org/x/sys/unix" || path == "golang.org/x/sys/windows") && (name == "Exec" || name == "ForkExec" || name == "StartProcess" || name == "CreateProcess" || name == "CreateProcessAsUser" || name == "ForkExecWithEnv")
				if forbidden {
					report(node, "raw process capability "+path+"."+name)
				}
			case *ast.SelectorExpr:
				selection := pkg.TypesInfo.Selections[node]
				if selection != nil && selection.Obj().Name() == "Env" && selection.Obj().Pkg() != nil && selection.Obj().Pkg().Path() == "os/exec" {
					report(node, "direct exec.Cmd.Env access")
				}
			case *ast.TypeSpec:
				if rawCommandType(pkg.TypesInfo.TypeOf(node.Type)) {
					report(node, "exec.Cmd derived value type")
				}
			case *ast.Field:
				if rawCommandType(pkg.TypesInfo.TypeOf(node.Type)) {
					report(node, "exec.Cmd value field")
				}
			case *ast.ArrayType:
				if rawCommandType(pkg.TypesInfo.TypeOf(node.Elt)) {
					report(node, "exec.Cmd value collection")
				}
			case *ast.MapType:
				if rawCommandType(pkg.TypesInfo.TypeOf(node.Value)) {
					report(node, "exec.Cmd value map")
				}
			case *ast.ChanType:
				if rawCommandType(pkg.TypesInfo.TypeOf(node.Value)) {
					report(node, "exec.Cmd value channel")
				}
			case *ast.CompositeLit:
				if rawCommandType(pkg.TypesInfo.TypeOf(node)) {
					report(node, "exec.Cmd literal")
				}
			case *ast.ValueSpec:
				if node.Type != nil && rawCommandType(pkg.TypesInfo.TypeOf(node.Type)) {
					report(node, "exec.Cmd value construction")
				}
			case *ast.CallExpr:
				if rawCommandType(pkg.TypesInfo.TypeOf(node)) {
					report(node, "exec.Cmd value conversion")
				}
				if id, ok := node.Fun.(*ast.Ident); ok {
					if builtin, ok := pkg.TypesInfo.Uses[id].(*types.Builtin); ok && builtin.Name() == "new" && len(node.Args) == 1 && rawCommandType(pkg.TypesInfo.TypeOf(node.Args[0])) {
						report(node, "exec.Cmd allocation")
					}
				}
			}
			return true
		})
	}
	return problems
}

func rawCommandType(t types.Type) bool {
	if t == nil {
		return false
	}
	named, ok := types.Unalias(t).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "os/exec" && named.Obj().Name() == "Cmd"
}

// These packages execute repository-controlled code rather than host tooling.
func pipelinePackage(path string) bool {
	for _, name := range []string{"repos", "pipelineentry", "nodehost", "engine"} {
		root := modulePath + "/internal/" + name
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}
