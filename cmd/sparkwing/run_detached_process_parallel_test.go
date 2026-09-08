package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestLongDetachedProcessRegressionsRunInParallel(t *testing.T) {
	targets := map[string]bool{
		"TestRunDetached_ExecutionOutlivesTheSubmittingProcess":         false,
		"TestRunsRetry_HeadlessLocalQueueExecutesFailedAndFullScopes":   false,
		"TestRunDetached_DuplicateKeyReturnsTheOriginalRun":             false,
		"TestRunDetached_DistinctKeysAreDistinctRuns":                   false,
		"TestRunDetached_RequestIDDoesNotDeduplicate":                   false,
		"TestRunDetached_PendingWorkRecoversAfterConsumerRestart":       false,
		"TestRunsConsumer_StatusAndStopReportTheResidentProcess":        false,
		"TestRunsCancel_CancelsAQueuedRunWithoutTouchingItsReplacement": false,
		"TestRunDetached_SeparatorHandsAConflictingFlagToThePipeline":   false,
		"TestRunDetached_RefusesAForegroundOnlyFlag":                    false,
		"TestRun_RefusesADetachedOnlyFlagWithoutDetached":               false,
		"TestRunsSubmit_IsNoLongerASubcommand":                          false,
		"TestRunDetached_RefusesAPipelineNothingDeclares":               false,
		"TestRunDetached_LiveDispatchSurvivesAWallClockJump":            false,
		"TestRunDetached_IdempotencyKeyDoesNotCrossPipelines":           false,
		"TestRunDetached_DuplicateKeyWithDifferentArgsIsRefused":        false,
		"TestRunDetached_DuplicateAckCarriesTheOriginalStatus":          false,
		"TestRunDetached_ReplacesAConsumerFromAnotherBuild":             false,
		"TestRunsConsumerStop_RecordsTheInterruptedRun":                 false,
	}
	file, err := parser.ParseFile(token.NewFileSet(), "run_detached_process_test.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if _, ok := targets[fn.Name.Name]; !ok {
			continue
		}
		targets[fn.Name.Name] = firstStatementIsParallel(fn)
	}
	for name, parallel := range targets {
		if !parallel {
			t.Errorf("%s must call t.Parallel() as its first statement", name)
		}
	}
}

func firstStatementIsParallel(fn *ast.FuncDecl) bool {
	if fn.Body == nil || len(fn.Body.List) == 0 {
		return false
	}
	expr, ok := fn.Body.List[0].(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := expr.X.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Parallel" {
		return false
	}
	receiver, ok := sel.X.(*ast.Ident)
	return ok && receiver.Name == "t"
}
