package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestConfigInspectDoesNotLoadPipelineYAML(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "config_inspect.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "runPipelineConfigInspect" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if ok && ident.Name == "loadPipelineYAML" {
				t.Errorf("runPipelineConfigInspect loads pipeline YAML for secret inspection")
			}
			return true
		})
		return
	}
	t.Fatal("runPipelineConfigInspect not found")
}
