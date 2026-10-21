package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

func spawnFindings(repo string) ([]string, error) {
	var problems []string
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64"} {
		platform, arch, _ := strings.Cut(target, "/")
		cfg := &packages.Config{Dir: repo, Env: append(os.Environ(), "GOOS="+platform, "GOARCH="+arch, "CGO_ENABLED=0"), Mode: packages.NeedName | packages.NeedFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedSyntax | packages.NeedTypesInfo}
		pkgs, err := packages.Load(cfg, "./...")
		if err != nil {
			return nil, err
		}
		for _, pkg := range pkgs {
			if len(pkg.Errors) > 0 {
				return nil, fmt.Errorf("spawn guard (%s): loading %s: %v", platform, pkg.PkgPath, pkg.Errors)
			}
			problems = append(problems, packageSpawnFindings(pkg)...)
		}
	}
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
				object := pkg.TypesInfo.Uses[node]
				if object == nil || object.Pkg() == nil {
					break
				}
				path, name := object.Pkg().Path(), object.Name()
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
