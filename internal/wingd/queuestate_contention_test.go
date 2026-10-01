package wingd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"testing"
)

func TestQueueStateCallbackLockPlacement(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "queuestate.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var callback *ast.FuncLit
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "readQueueState" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok && types.ExprString(call.Fun) == "d.queueStateReads.Do" && len(call.Args) == 2 {
				callback, _ = call.Args[1].(*ast.FuncLit)
			}
			return true
		})
	}
	if callback == nil {
		t.Fatal("queue state callback not found")
	}
	locked := false
	seen := map[string]bool{}
	for _, stmt := range callback.Body.List {
		var expressions []ast.Expr
		switch stmt := stmt.(type) {
		case *ast.ExprStmt:
			expressions = []ast.Expr{stmt.X}
		case *ast.AssignStmt:
			expressions = stmt.Rhs
		case *ast.ReturnStmt:
			for _, result := range stmt.Results {
				if _, ok := result.(*ast.Ident); !ok {
					t.Fatalf("review lock ownership for return expression %T", result)
				}
			}
			if locked {
				t.Fatal("queue state returns while holding daemon lock")
			}
		default:
			t.Fatalf("review lock ownership for callback statement %T", stmt)
		}
		for _, expr := range expressions {
			call, ok := expr.(*ast.CallExpr)
			if !ok {
				t.Fatalf("review lock ownership for callback expression %T", expr)
			}
			name := types.ExprString(call.Fun)
			switch name {
			case "d.mu.Lock":
				if locked {
					t.Fatal("daemon lock acquired twice")
				}
				locked = true
			case "d.mu.Unlock":
				if !locked {
					t.Fatal("daemon lock released without ownership")
				}
				locked = false
			case "d.buildQueueStateLocked":
				if !locked {
					t.Fatal("queue snapshot requires daemon lock")
				}
			case "annotateETA", "annotateSemaphoreETA":
				if locked || !seen["d.buildQueueStateLocked"] {
					t.Fatalf("%s requires a completed snapshot and released daemon lock", name)
				}
			default:
				t.Fatalf("review lock ownership for callback call %s", name)
			}
			seen[name] = true
		}
	}
	for _, name := range []string{"d.buildQueueStateLocked", "annotateETA", "annotateSemaphoreETA"} {
		if !seen[name] {
			t.Errorf("missing %s", name)
		}
	}
}
