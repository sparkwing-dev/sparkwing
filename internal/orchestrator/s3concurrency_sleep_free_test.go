package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestS3ConcurrencyBurstRegressionsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"runS3ConcurrencyBurst":                  false,
		"TestS3Concurrency_NoOverAdmission":      false,
		"TestS3Concurrency_NoOverBudgetWithCost": false,
		"TestS3Concurrency_LeaseExpiryReclaimed": false,
		"ExpireS3ConcurrencyHolderForTest":       false,
	}
	fset := token.NewFileSet()
	for _, path := range []string{"s3concurrency_test.go", "s3concurrency_expiry_export_test.go"} {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			if path == "s3concurrency_expiry_export_test.go" {
				continue
			}
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			if _, ok := targets[fn.Name.Name]; !ok {
				continue
			}
			targets[fn.Name.Name] = true
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Sleep" {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if ok && pkg.Name == "time" {
					t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
				}
				return true
			})
		}
	}

	for name, found := range targets {
		if !found {
			t.Errorf("S3 concurrency test fixtures do not declare %s", name)
		}
	}
}
