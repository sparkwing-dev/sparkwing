package orchestrator_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestConcurrencySemanticsDoNotUseTimeSleep(t *testing.T) {
	targets := map[string]bool{
		"semStep":                                     false,
		"waitForSemConcurrencyPopulation":             false,
		"TestMemo_InFlightDedupeOnContent":            false,
		"TestScope_BoxSerializesAcrossRunsOnSameHost": false,
		"TestConcurrency_CostSummedAcrossBoxScope":    false,
		"TestConcurrency_WaitDoesNotHoldWorkerSlot":   false,
	}
	gateOwners := map[string]bool{
		"TestMemo_InFlightDedupeOnContent":            false,
		"TestScope_BoxSerializesAcrossRunsOnSameHost": false,
		"TestConcurrency_CostSummedAcrossBoxScope":    false,
		"TestConcurrency_WaitDoesNotHoldWorkerSlot":   false,
	}
	populationOwners := map[string]bool{
		"TestScope_BoxSerializesAcrossRunsOnSameHost": true,
		"TestConcurrency_CostSummedAcrossBoxScope":    true,
		"TestConcurrency_WaitDoesNotHoldWorkerSlot":   true,
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "concurrency_semantics_test.go", nil, 0)
	if err != nil {
		t.Fatalf("parse concurrency_semantics_test.go: %v", err)
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
			if ident, ok := call.Fun.(*ast.Ident); ok {
				switch ident.Name {
				case "installSemStepGate":
					if _, owner := gateOwners[fn.Name.Name]; owner {
						gateOwners[fn.Name.Name] = true
					}
				case "waitForSemConcurrencyPopulation":
					if populationOwners[fn.Name.Name] {
						populationOwners[fn.Name.Name] = false
					}
				}
			}
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Sleep" {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" {
					t.Errorf("%s contains time.Sleep at %s", fn.Name.Name, fset.Position(call.Pos()))
				}
			}
			return true
		})
	}
	for name, found := range targets {
		if !found {
			t.Errorf("concurrency_semantics_test.go does not declare %s", name)
		}
	}
	for name, usesGate := range gateOwners {
		if !usesGate {
			t.Errorf("%s does not install the sem-step gate", name)
		}
	}
	for name, missingPopulation := range populationOwners {
		if missingPopulation {
			t.Errorf("%s does not observe its concurrency population", name)
		}
	}
}
