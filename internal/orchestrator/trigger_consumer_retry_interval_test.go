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
		"serveConsumerContending":                                       false,
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
			var matchesCall func(*ast.CallExpr) bool
			switch fn.Name.Name {
			case "RunLocalTriggerConsumer":
				matchesCall = func(call *ast.CallExpr) bool {
					return callsWithLastArg(call, "runLocalTriggerConsumerWithRetryInterval", "consumerElectionRetryInterval")
				}
			case "runLocalTriggerConsumerWithRetryInterval":
				matchesCall = func(call *ast.CallExpr) bool {
					return callsWithLastArg(call, "serveConsumerContending", "retryInterval")
				}
			case "serveConsumerContending":
				matchesCall = func(call *ast.CallExpr) bool {
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return false
					}
					pkg, pkgOK := sel.X.(*ast.Ident)
					return pkgOK && pkg.Name == "time" && sel.Sel.Name == "After" &&
						len(call.Args) == 1 && isIdent(call.Args[0], "retryInterval")
				}
			case "TestDashboardConsumer_RetakesTheQueueAfterTheResidentIdlesOut":
				matchesCall = func(call *ast.CallExpr) bool {
					name, ok := call.Fun.(*ast.Ident)
					return ok && name.Name == "runLocalTriggerConsumerWithRetryInterval" &&
						len(call.Args) == 5 && isTenMilliseconds(call.Args[4])
				}
			}
			found := false
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				found = found || matchesCall(call)
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

func callsWithLastArg(call *ast.CallExpr, name, arg string) bool {
	callee, ok := call.Fun.(*ast.Ident)
	return ok && callee.Name == name && len(call.Args) == 5 && isIdent(call.Args[4], arg)
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
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
