package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

const failedTestError = `command failed (exit 1): echo "running tests"; if [ -f /tmp/flags/fail-test ]; then echo "FAIL: TestWidget expected 3 got 4" >&2; exit 1; fi; echo "ok all tests"` + "\nFAIL: TestWidget expected 3 got 4"

type streamTestReader struct {
	fakeLogReader
	controllerwire.RunReader
	detail controllerwire.RunDetail
}

func (r streamTestReader) ReadRun(context.Context, string) (controllerwire.RunDetail, error) {
	detail := r.detail
	detail.Steps = nil
	return detail, nil
}

func (r streamTestReader) ReadSteps(context.Context, string) (controllerwire.RunSteps, error) {
	return controllerwire.RunSteps{Steps: r.detail.Steps}, nil
}

func failedShipDetail() controllerwire.RunDetail {
	return controllerwire.RunDetail{
		ID: "run-1", Number: 2, Status: "failed", Failure: "failed: test; cancelled: deploy",
		Jobs: []controllerwire.JobDetail{
			{ID: "build", Status: "done", Outcome: "success", Attempt: 1},
			{ID: "lint", Status: "done", Outcome: "success", Attempt: 1},
			{ID: "test", Status: "done", Outcome: "failed", Reason: "sdk_error", Error: failedTestError, Attempt: 1, Needs: []string{"build"}},
			{ID: "deploy", Status: "done", Outcome: "cancelled", Reason: "upstream_failed", Needs: []string{"test", "lint"}},
		},
		Steps: []controllerwire.StepDetail{
			{JobID: "test", ID: "unit", Attempt: 1, Status: "failed", Outcome: "failed"},
		},
	}
}

func attemptRecord(job string, attempt int64, line string) controllerwire.LogRecord {
	return controllerwire.LogRecord{RunID: "run-1", LineNo: 1, Line: line, Subject: &blob.LogSubject{Kind: blob.LogJobAttempt, Team: "t", RunID: "run-1", Attempt: &runspec.AttemptIdentity{Team: "t", RunID: "run-1", NodeID: job, Attempt: attempt, Generation: 1}}}
}

func failedShipRecords() []controllerwire.LogRecord {
	frame := func(step, event, msg, attrs string) string {
		line := fmt.Sprintf(`{"ts":"2026-11-14T06:34:36Z","level":"info","step":%q,"event":%q,"msg":%q`, step, event, msg)
		if attrs != "" {
			line += `,"attrs":` + attrs
		}
		return line + "}"
	}
	return []controllerwire.LogRecord{
		{RunID: "run-1", Event: "run_started", LineNo: 1, Line: `{"reason":""}`},
		{RunID: "run-1", Event: "job_ready", JobID: "test", LineNo: 2, Line: "null"},
		{RunID: "run-1", Event: "job_claimed", JobID: "test", LineNo: 3, Line: "null"},
		{RunID: "run-1", Event: "job_admission_granted", JobID: "test", LineNo: 4, Line: `{"wait_ms":0}`},
		{RunID: "run-1", Event: "job_started", JobID: "test", LineNo: 5, Line: "null"},
		attemptRecord("test", 1, frame("unit", "step_start", "unit", `{"step":"unit"}`)),
		attemptRecord("test", 1, frame("unit", "exec_line", "running tests", "")),
		attemptRecord("test", 1, frame("unit", "exec_line", "FAIL: TestWidget expected 3 got 4", "")),
		attemptRecord("test", 1, frame("unit", "step_end", "unit", fmt.Sprintf(`{"duration_ms":23,"error":%q,"outcome":"failed","step":"unit"}`, failedTestError))),
		attemptRecord("test", 1, `{"ts":"2026-11-14T06:34:36Z","level":"info","event":"work_fail_fast","msg":"unit","attrs":{"cancellation_latency_ms":0,"cancelled_siblings":0,"cancelled_steps":null,"trigger_step":"unit"}}`),
		attemptRecord("test", 2, frame("unit", "step_end", "unit", `{"duration_ms":1500,"outcome":"success","step":"unit"}`)),
		{RunID: "run-1", Event: "job_failed", JobID: "test", LineNo: 6, Line: fmt.Sprintf(`{"Outcome":"failed","Reason":"sdk_error","MaskedError":%q}`, failedTestError)},
		{RunID: "run-1", LineNo: 6, Line: `{"ts":"2026-11-14T06:34:31Z","level":"info","event":"preparation","msg":"source preparation","attrs":{"stage":"checkout","duration_ms":17}}`, Subject: &blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "t", RunID: "run-1", Unit: &blob.LogUnit{Kind: "trigger", Generation: 1}}},
		{RunID: "run-1", Event: "job_admission_queued", JobID: "deploy", LineNo: 7, Line: `{"blocking_reason":"needs 1.0 cores; 0.0 available","position":1,"wait_ms":0}`},
		{RunID: "run-1", Event: "job_cancelled", JobID: "deploy", LineNo: 7, Line: `{"Outcome":"cancelled","Reason":"upstream_failed","MaskedError":""}`},
		{RunID: "run-1", Event: "run_failed", LineNo: 8, Line: `{"reason":"failed: test; cancelled: deploy"}`},
	}
}

func TestPrettyRunStreamLabelsJobsAndEndsWithTheFailureBlock(t *testing.T) {
	reader := streamTestReader{detail: failedShipDetail()}
	reader.read = func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{Complete: true, Records: failedShipRecords()}, nil
	}
	var out bytes.Buffer
	if err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "2", "-o", "pretty"), reader, reader, &out, &out, nil); err != nil {
		t.Fatal(err)
	}
	want := `test | unit started
test | running tests
test | FAIL: TestWidget expected 3 got 4
test | unit failed after 23ms
test#2 | unit succeeded in 1.5s
deploy | waiting for admission: needs 1.0 cores; 0.0 available
deploy | cancelled: test did not succeed
run | failed: test; cancelled: deploy

test failed in step unit:
  command failed (exit 1): echo "running tests"; if [ -f /tmp/flags/fail-test ]; then echo "FAIL: TestWidget expected 3 got 4" >&2; exit 1; fi; echo "ok all te...
  FAIL: TestWidget expected 3 got 4
deploy was cancelled because test did not succeed

Show the failed job's log:  sparkwing runs logs --run 2 --job test
Retry the failed jobs:      sparkwing runs retry --failed --run 2
`
	if out.String() != want {
		t.Fatalf("pretty stream:\n%s\nwant:\n%s", &out, want)
	}
}

func TestPrettyStreamKeepsMultiLineTextOnLabelledLinesAndQuotesControls(t *testing.T) {
	var out bytes.Buffer
	record := attemptRecord("build", 1, `{"ts":"2026-11-14T06:34:36Z","level":"warn","step":"compile","event":"note","msg":"two\nlines \u001b[2J"}`)
	if err := writeLogRecord(&out, record, "pretty", false); err != nil {
		t.Fatal(err)
	}
	if want := "build | two\nbuild |   \"lines \\x1b[2J\"\n"; out.String() != want {
		t.Fatalf("pretty line = %q, want %q", &out, want)
	}
}

func TestForegroundFailedRunPrintsTheBlockInsteadOfADiagnostic(t *testing.T) {
	reader := streamTestReader{detail: failedShipDetail()}
	var out bytes.Buffer
	err := foregroundRunCompletion(t.Context(), reader, "run-1", "failed", "pretty", &out)
	var reported *cliError
	if !errors.As(err, &reported) || !reported.reported || exitCodeFor(err) != 1 {
		t.Fatalf("completion error = %#v, want a reported exit 1", err)
	}
	if !strings.HasPrefix(out.String(), "\ntest failed in step unit:\n") || !strings.HasSuffix(out.String(), "sparkwing runs retry --failed --run 2\n") {
		t.Fatalf("failure block:\n%s", &out)
	}
	out.Reset()
	if err := foregroundRunCompletion(t.Context(), reader, "run-1", "failed", "json", &out); !errors.As(err, &reported) || reported.reported || out.Len() != 0 {
		t.Fatalf("json completion changed: err=%v out=%q", err, &out)
	}
}

func TestLocalRunKeepsInfoProcessLogsOffTheTerminal(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(previous) })
	deps := localRunDependencies{resolve: func(ctx context.Context, _ localRunOptions) (localRunRuntime, error) {
		slog.InfoContext(ctx, "source preparation", "stage", "checkout")
		slog.WarnContext(ctx, "runner restarted")
		return localRunRuntime{}, errors.New("stop after resolve")
	}}
	if err := runLocalCommand(t.Context(), "ci", runFlags{}, nil, &bytes.Buffer{}, deps); err == nil {
		t.Fatal("resolve failure was lost")
	}
	if strings.Contains(logged.String(), "source preparation") || !strings.Contains(logged.String(), "runner restarted") {
		t.Fatalf("terminal process logs:\n%s", &logged)
	}
	slog.Info("after the run")
	if !strings.Contains(logged.String(), "after the run") {
		t.Fatal("the run left the process log level lowered")
	}
}

func TestJobScopedFailureBlockOmitsItsOwnLogCommand(t *testing.T) {
	var out bytes.Buffer
	if err := writeFailureBlock(&out, failedShipDetail(), "test", "cloud"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "deploy") || strings.Contains(out.String(), "runs logs") || !strings.HasSuffix(out.String(), "Retry the failed jobs:      sparkwing runs retry --failed --run 2 --profile cloud\n") {
		t.Fatalf("job-scoped block:\n%s", &out)
	}
}

func TestPrettyStreamShowsASecretRefusalAsItsMessage(t *testing.T) {
	var out bytes.Buffer
	record := controllerwire.LogRecord{RunID: "run-1", Event: "secret_preflight_refused", JobID: "plan", LineNo: 1, Line: `{"message":"pipeline \"ship\" did not start.\n- DEPLOY_TOKEN is unavailable.\n  Fix: sparkwing secrets set --secret DEPLOY_TOKEN --file <path>"}`}
	if err := writeLogRecord(&out, record, "pretty", false); err != nil {
		t.Fatal(err)
	}
	want := "plan | pipeline \"ship\" did not start.\nplan |   - DEPLOY_TOKEN is unavailable.\nplan |     Fix: sparkwing secrets set --secret DEPLOY_TOKEN --file <path>\n"
	if out.String() != want {
		t.Fatalf("refusal line = %q, want %q", &out, want)
	}
}
