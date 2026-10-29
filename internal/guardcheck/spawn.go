package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"slices"
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
				if path == modulePath+"/internal/procenv" && pkg.PkgPath != path && !hostCommandPackage(pkg.PkgPath) {
					switch name {
					case "HostCommand", "HostCommandContext", "ControlCommand", "ControlCommandContext", "ConfigureWindowsBash", "Replace":
						report(node, "a package outside hostCommandPackages requires procenv.Command or CommandContext with procenv.Env, not "+name)
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

// hostCommandPackages run host tooling, so their children may inherit the
// host or controller environment. Every other package, including one added
// later and those that run repository code (repos, pipelineentry, nodehost,
// engine), builds its environment through procenv.Command; joining this list
// is a reviewed decision.
var hostCommandPackages = []string{
	"bin/qagoproxy",
	"cmd/sparkwing",
	"cmd/verify-release",
	"internal/acceptance",
	"internal/bincache",
	"internal/chaos/crashdummy",
	"internal/chaos/soakguard",
	"internal/cluster",
	"internal/crons",
	"internal/crontimer",
	"internal/doccheck",
	"internal/gatescope",
	"internal/gitcmd",
	"internal/gotoolchain",
	"internal/guardcheck",
	"internal/pipelineaccept",
	"internal/pipelinegen",
	"internal/procgroup",
	"internal/runners/service",
	"internal/sessionledger",
	"internal/source",
	"internal/source/sourcetest",
	"internal/sparks",
	"internal/testleak",
	"internal/testshell",
	"internal/wingd",
	"internal/wingd/client",
	"sparkwing",
	"sparkwing/docker",
	"sparkwing/git",
	"sparkwing/services",
}

func hostCommandPackage(path string) bool {
	rel, ok := strings.CutPrefix(path, modulePath+"/")
	return ok && slices.Contains(hostCommandPackages, rel)
}
