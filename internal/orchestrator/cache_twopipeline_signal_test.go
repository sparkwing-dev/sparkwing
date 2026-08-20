package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestSharedS3SerializationUsesStateSignalsInsteadOfDuration(t *testing.T) {
	targets := map[string]bool{
		"s3Push":           false,
		"runSharedS3Burst": false,
		"TestCache_TwoPipelinesShareKey_PushSerializes":       false,
		"TestCache_TwoPipelinesShareKey_AcrossMultipleBursts": false,
	}
	usesBurst := map[string]bool{}
	usesPopulation := false
	signals := map[string]map[string]bool{}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "cache_twopipeline_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = true
		signals[fn.Name.Name] = map[string]bool{}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "runSharedS3Burst" {
				usesBurst[fn.Name.Name] = true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok && ident.Name == "waitForCacheConcurrencyPopulation" && fn.Name.Name == "runSharedS3Burst" {
				usesPopulation = true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if ok && pkg.Name == "time" && (sel.Sel.Name == "Sleep" || sel.Sel.Name == "After" || sel.Sel.Name == "NewTicker") {
				t.Errorf("%s contains time.%s at %s", fn.Name.Name, sel.Sel.Name, fset.Position(call.Pos()))
			}
			return true
		})
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			sel, ok := node.(*ast.SelectorExpr)
			if ok && (sel.Sel.Name == "entered" || sel.Sel.Name == "release" || sel.Sel.Name == "finished") {
				signals[fn.Name.Name][sel.Sel.Name] = true
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("cache_twopipeline_test.go does not declare %s", name)
		}
	}
	for _, name := range []string{
		"TestCache_TwoPipelinesShareKey_PushSerializes",
		"TestCache_TwoPipelinesShareKey_AcrossMultipleBursts",
	} {
		if !usesBurst[name] {
			t.Errorf("%s does not delegate to runSharedS3Burst", name)
		}
	}
	if !usesPopulation {
		t.Error("runSharedS3Burst does not observe the authoritative concurrency population")
	}
	for _, name := range []string{"s3Push", "runSharedS3Burst"} {
		for _, signal := range []string{"entered", "release", "finished"} {
			if !signals[name][signal] {
				t.Errorf("%s does not use the gate's %s signal", name, signal)
			}
		}
	}
}
