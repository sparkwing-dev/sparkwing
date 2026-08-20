package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestDashboardConsumerRetakeUsesControlledElectionRetryInterval(t *testing.T) {
	fset := token.NewFileSet()
	files := []string{"trigger_consumer.go", "trigger_consumer_test.go"}
	targets := map[string]bool{
		"RunLocalTriggerConsumer":                                       false,
		"runLocalTriggerConsumerWithRetryInterval":                      false,
		"TestDashboardConsumer_RetakesTheQueueAfterTheResidentIdlesOut": false,
	}
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
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
			var matchesArg func(ast.Expr) bool
			switch fn.Name.Name {
			case "RunLocalTriggerConsumer":
				matchesArg = func(expr ast.Expr) bool {
					id, ok := expr.(*ast.Ident)
					return ok && id.Name == "consumerElectionRetryInterval"
				}
			case "TestDashboardConsumer_RetakesTheQueueAfterTheResidentIdlesOut":
				matchesArg = isTenMilliseconds
			default:
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok || name.Name != "runLocalTriggerConsumerWithRetryInterval" || len(call.Args) != 5 {
					return true
				}
				found = matchesArg(call.Args[4])
				return true
			})
			if !found {
				t.Errorf("%s must delegate with its required retry interval", fn.Name.Name)
			}
		}
	}
	for name, found := range targets {
		if !found {
			t.Errorf("%s not found", name)
		}
	}
}

func isTenMilliseconds(expr ast.Expr) bool {
	mul, ok := expr.(*ast.BinaryExpr)
	if !ok || mul.Op != token.MUL {
		return false
	}
	n, ok := mul.X.(*ast.BasicLit)
	if !ok || n.Value != "10" {
		return false
	}
	sel, ok := mul.Y.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Millisecond" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}
