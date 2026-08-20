package orchestrator

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestWingdToolSlotProgressDoesNotUseTimeSleep(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "wingd_toolslot_e2e_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse wingd_toolslot_e2e_test.go: %v", err)
	}
	found := false
	usesPausedCheck := false
	usesForcedExpiry := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestWingd_NoProgressTimeoutPausesForToolSlotAndResumesAfterGrant" {
			continue
		}
		found = true
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			if ident, ok := call.Fun.(*ast.Ident); ok {
				switch ident.Name {
				case "ProgressTimeoutPausedForTest":
					usesPausedCheck = true
				case "ExpireProgressTimeoutForTest":
					usesForcedExpiry = true
				}
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && sel.Sel.Name == "Sleep" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
					t.Errorf("tool-slot progress test uses time.Sleep at %s", fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
	if !found {
		t.Fatal("tool-slot progress test declaration not found")
	}
	if !usesPausedCheck {
		t.Error("tool-slot progress test does not inspect the paused timeout controller")
	}
	if !usesForcedExpiry {
		t.Error("tool-slot progress test does not force timeout expiry")
	}
}
