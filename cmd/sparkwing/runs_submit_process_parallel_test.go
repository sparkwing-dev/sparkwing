package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestLongSubmitProcessRegressionsRunInParallel(t *testing.T) {
	targets := map[string]bool{
		"TestRunsSubmit_ExecutionOutlivesTheSubmittingProcess":          false,
		"TestRunsSubmit_DuplicateKeyReturnsTheOriginalRun":              false,
		"TestRunsSubmit_DistinctKeysAreDistinctRuns":                    false,
		"TestRunsSubmit_RequestIDDoesNotDeduplicate":                    false,
		"TestRunsSubmit_PendingWorkRecoversAfterConsumerRestart":        false,
		"TestRunsConsumer_StatusAndStopReportTheResidentProcess":        false,
		"TestRunsCancel_CancelsAQueuedRunWithoutTouchingItsReplacement": false,
		"TestRunsSubmit_RefusesASubmitFlagPlacedAfterThePipelineName":   false,
		"TestRunsSubmit_SeparatorHandsAConflictingFlagToThePipeline":    false,
		"TestRunsSubmit_RefusesAPipelineNothingDeclares":                false,
		"TestRunsSubmit_LiveDispatchSurvivesAWallClockJump":             false,
		"TestRunsSubmit_IdempotencyKeyDoesNotCrossPipelines":            false,
		"TestRunsSubmit_DuplicateKeyWithDifferentArgsIsRefused":         false,
		"TestRunsSubmit_DuplicateAckCarriesTheOriginalStatus":           false,
		"TestRunsSubmit_ReplacesAConsumerFromAnotherBuild":              false,
		"TestRunsConsumerStop_RecordsTheInterruptedRun":                 false,
	}
	file, err := parser.ParseFile(token.NewFileSet(), "runs_submit_process_test.go", nil, 0)
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
