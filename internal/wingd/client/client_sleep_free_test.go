package client

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSlowSpawnRegressionDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "client_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse client_test.go: %v", err)
	}
	found := false
	closesSpawnRequested := false
	spawnRequestedReceives := 0
	startDaemonReceives := 0
	releaseStartCalls := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestEnsureDaemon_WaitsForOneSlowHealthySpawn" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if receive, ok := node.(*ast.UnaryExpr); ok && receive.Op == token.ARROW {
				if channel, ok := receive.X.(*ast.Ident); ok {
					switch channel.Name {
					case "spawnRequested":
						spawnRequestedReceives++
					case "startDaemon":
						startDaemonReceives++
					}
				}
			}
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				switch ident.Name {
				case "close":
					if len(call.Args) == 1 {
						if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "spawnRequested" {
							closesSpawnRequested = true
						}
					}
				case "releaseStart":
					releaseStartCalls++
				}
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Sleep" {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" {
				t.Errorf("slow-spawn regression uses time.Sleep at %s", fset.Position(call.Pos()))
			}
			return true
		})
	}
	if !found {
		t.Fatal("TestEnsureDaemon_WaitsForOneSlowHealthySpawn declaration not found")
	}
	if !closesSpawnRequested || spawnRequestedReceives < 2 || startDaemonReceives == 0 || releaseStartCalls < 2 {
		t.Fatal("slow-spawn regression must gate daemon startup after the spawn request and release it on normal and cleanup paths")
	}
}
