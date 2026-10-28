package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type fakeLogReader struct {
	read   func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error)
	search func(context.Context, controllerwire.LogSearchQuery) (controllerwire.LogSearchPage, error)
}

func (f fakeLogReader) ReadLogs(ctx context.Context, run string, q controllerwire.LogQuery) (controllerwire.LogPage, error) {
	return f.read(ctx, run, q)
}

func (f fakeLogReader) SearchLogs(ctx context.Context, q controllerwire.LogSearchQuery) (controllerwire.LogSearchPage, error) {
	return f.search(ctx, q)
}

func logInvocation(t *testing.T, args ...string) clisurface.Invocation {
	t.Helper()
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func jobLogRecord(run, line string) controllerwire.LogRecord {
	return controllerwire.LogRecord{RunID: run, LineNo: 3, Line: line, Subject: &blob.LogSubject{Kind: blob.LogJobAttempt, Team: "t1", RunID: run, Attempt: &runspec.AttemptIdentity{Team: "t1", RunID: run, NodeID: "build", Attempt: 2, Generation: 1}}}
}

func TestRunLogsPagesAndFollowsUntilPersistedCompletion(t *testing.T) {
	var output, diagnostics bytes.Buffer
	calls, waits := 0, 0
	reader := fakeLogReader{read: func(_ context.Context, run string, q controllerwire.LogQuery) (controllerwire.LogPage, error) {
		if run != "412" || !q.Tree || q.Tail == nil || *q.Tail != 0 {
			t.Fatalf("selection changed: run=%s query=%+v", run, q)
		}
		calls++
		switch calls {
		case 1:
			return controllerwire.LogPage{Records: []controllerwire.LogRecord{jobLogRecord("r1", "first")}, Truncated: true, NextCursor: "page-2"}, nil
		case 2:
			if q.Cursor != "page-2" {
				t.Fatalf("cursor = %s", q.Cursor)
			}
			return controllerwire.LogPage{NextCursor: "page-2"}, nil
		default:
			if q.Cursor != "page-2" {
				t.Fatalf("cursor = %s", q.Cursor)
			}
			return controllerwire.LogPage{Records: []controllerwire.LogRecord{{RunID: "child", Subject: &blob.LogSubject{Kind: blob.LogOwnerUnit, Team: "t1", RunID: "child", Unit: &blob.LogUnit{Kind: blob.LogPlan, Generation: 1}}, LineNo: 1, Line: "planned"}}, Complete: true}, nil
		}
	}}
	err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "412", "--tree", "--tail", "0", "--follow", "-o", "json"), reader, nil, &output, &diagnostics, func(context.Context, time.Duration) error { waits++; return nil })
	if err != nil || calls != 3 || waits != 1 || !strings.Contains(output.String(), `"kind":"owner_unit"`) || !strings.Contains(output.String(), `"job_id":"build"`) || strings.Contains(output.String(), "node_id") {
		t.Fatalf("calls=%d waits=%d output=%s err=%v", calls, waits, &output, err)
	}
}

func TestRunLogSearchPreservesFiltersMatchesAndContinuation(t *testing.T) {
	for _, verb := range []string{"logs", "grep"} {
		t.Run(verb, func(t *testing.T) {
			var output, diagnostics bytes.Buffer
			reader := fakeLogReader{search: func(_ context.Context, q controllerwire.LogSearchQuery) (controllerwire.LogSearchPage, error) {
				if q.Pattern != "fatal" || q.Job != "build" || q.MaxMatches != 0 || q.Runs.Limit != 7 || q.Runs.Cursor != "old" || q.Runs.Pipeline != "test" || q.Runs.Status != "failed" || q.Runs.SHA != "abc" || strings.Join(q.Runs.Branches, ",") != "main,!release" || q.Runs.StartedAfter.Format("2006-01-02") != "2026-10-01" {
					t.Fatalf("query = %+v", q)
				}
				return controllerwire.LogSearchPage{Matches: []controllerwire.LogRecord{jobLogRecord("r1", "fatal error"), jobLogRecord("r1", "fatal again"), jobLogRecord("r2", "fatal")}, Truncated: true, NextCursor: "next"}, nil
			}}
			inv := logInvocation(t, verb, "--grep", "fatal", "--job", "build", "--max-matches", "0", "--limit", "7", "--cursor", "old", "--pipeline", "test", "--status", "failed", "--sha", "abc", "--branch", "main,!release", "--started-after", "2026-10-01", "-q", "-o", "plain")
			if err := observeRunLogs(t.Context(), inv, reader, nil, &output, &diagnostics, nil); err != nil {
				t.Fatal(err)
			}
			if output.String() != "r1\nr2\n" || !strings.Contains(diagnostics.String(), "next") {
				t.Fatalf("output=%q diagnostics=%q", output.String(), diagnostics.String())
			}
		})
	}
}

func TestRunLogsRejectInvalidUsageBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{
		{}, {"--run", "r1", "--pipeline", "build"}, {"--run", "r1", "--tail", "0", "--head", "0"}, {"--run", "r1", "--tail", "-1"}, {"--run", "r1", "--lines", "3:2"}, {"--grep", "fatal", "--follow"}, {"--grep", "fatal", "--max-matches", "-1"},
	} {
		if err := runRunsLogsArgs(t.Context(), "logs", args); exitCodeFor(err) != 2 {
			t.Fatalf("args=%v err=%v exit=%d", args, err, exitCodeFor(err))
		}
	}
}

func TestRunLogsPropagatesFailuresAndIncompleteFollow(t *testing.T) {
	var out bytes.Buffer
	failure := &apierr.Error{Code: apierr.Forbidden, Message: "revoked"}
	reader := fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{}, failure
	}}
	err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "r1"), reader, nil, &out, &out, nil)
	if !errors.Is(err, failure) || exitCodeFor(err) != 3 {
		t.Fatalf("error = %v", err)
	}
	reader.read = func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{NextCursor: "live"}, nil
	}
	err = observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "r1", "--follow"), reader, nil, &out, &out, func(context.Context, time.Duration) error { return context.Canceled })
	if !errors.Is(err, context.Canceled) || exitCodeFor(err) != 3 {
		t.Fatalf("error = %v", err)
	}
	reader.read = func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{Records: []controllerwire.LogRecord{jobLogRecord("r1", "hello")}, NextCursor: "same", Truncated: true}, nil
	}
	err = observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "r1", "--cursor", "same"), reader, nil, &out, &out, nil)
	if exitCodeFor(err) != 3 {
		t.Fatalf("repeated cursor error = %v", err)
	}
}

func TestRunLogSearchLookbackAndEmptyPageRemainExplicit(t *testing.T) {
	inv := logInvocation(t, "grep", "--grep", "fatal", "--since", "7d", "--limit", "5001", "-q", "-o", "json")
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	query, err := parseLogSearch(inv, now)
	if err != nil || !query.Runs.StartedAfter.Equal(now.Add(-7*24*time.Hour)) || query.Runs.Limit != 5001 || query.MaxMatches != 5 {
		t.Fatalf("query=%+v err=%v", query, err)
	}
	reader := fakeLogReader{search: func(context.Context, controllerwire.LogSearchQuery) (controllerwire.LogSearchPage, error) {
		return controllerwire.LogSearchPage{Truncated: true, NextCursor: "older-runs"}, nil
	}}
	var out, diagnostics bytes.Buffer
	if err := observeRunLogs(t.Context(), inv, reader, nil, &out, &diagnostics, nil); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), `"next_cursor":"older-runs"`) {
		t.Fatalf("out=%q diagnostics=%q", out.String(), diagnostics.String())
	}
}

func TestRunLogsRetainsSelectionAndPlainOutput(t *testing.T) {
	inv := logInvocation(t, "logs", "--run", "412", "--job", "build", "--head", "2", "--lines", "2:4", "--grep", "fatal", "--since", "7d", "--no-events", "-o", "plain")
	reader := fakeLogReader{read: func(_ context.Context, run string, query controllerwire.LogQuery) (controllerwire.LogPage, error) {
		if run != "412" || query.Job != "build" || query.Head == nil || *query.Head != 2 || query.Lines != "2:4" || query.Grep != "fatal" || query.Since != 7*24*time.Hour || !query.NoEvents {
			t.Fatalf("run=%s query=%+v", run, query)
		}
		return controllerwire.LogPage{Records: []controllerwire.LogRecord{jobLogRecord("r1", "fatal error")}, Complete: true}, nil
	}}
	var output bytes.Buffer
	if err := observeRunLogs(t.Context(), inv, reader, nil, &output, &output, nil); err != nil || output.String() != "fatal error\n" {
		t.Fatalf("output=%q err=%v", output.String(), err)
	}
}

func TestRunLogsDistinguishesMissingRunFromEmptyLogs(t *testing.T) {
	reader := fakeLogReader{read: func(_ context.Context, run string, _ controllerwire.LogQuery) (controllerwire.LogPage, error) {
		if run == "missing" {
			return controllerwire.LogPage{}, &apierr.Error{Code: apierr.NotFound, Message: "run not found"}
		}
		return controllerwire.LogPage{Complete: true}, nil
	}}
	var output bytes.Buffer
	if err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "present"), reader, nil, &output, &output, nil); err != nil {
		t.Fatalf("empty logs: %v", err)
	}
	err := observeRunLogs(t.Context(), logInvocation(t, "logs", "--run", "missing"), reader, nil, &output, &output, nil)
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Code != apierr.NotFound || exitCodeFor(err) != 3 {
		t.Fatalf("missing logs: %v", err)
	}
}

func TestRunLogSearchQuietJSONUsesObjects(t *testing.T) {
	reader := fakeLogReader{search: func(context.Context, controllerwire.LogSearchQuery) (controllerwire.LogSearchPage, error) {
		return controllerwire.LogSearchPage{Matches: []controllerwire.LogRecord{jobLogRecord("r1", "fatal"), jobLogRecord("r1", "fatal again")}}, nil
	}}
	var out, diagnostics bytes.Buffer
	if err := observeRunLogs(t.Context(), logInvocation(t, "grep", "--grep", "fatal", "-q", "-o", "json"), reader, nil, &out, &diagnostics, nil); err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["run_id"] != "r1" || len(got) != 1 {
		t.Fatalf("quiet result=%s err=%v", &out, err)
	}
}

type logRoundTrip func(*http.Request) (*http.Response, error)

func (f logRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestRunLogsTriggerFollowPreservesTransportAndBearer(t *testing.T) {
	var out bytes.Buffer
	options := client.EngineOptions{URL: "https://controller.example", Bearer: func(context.Context) (string, error) { return "secret", nil }, HTTP: &http.Client{Transport: logRoundTrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != "/api/v1/runs/r1/logs" || req.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("request = %s", req.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"records":[],"next_cursor":"","truncated":false,"complete":true}`)), Header: make(http.Header)}, nil
	})}}
	if err := followControllerRunLogs(t.Context(), options, "r1", "plain", &out); err != nil || out.Len() != 0 {
		t.Fatalf("follow=%q err=%v", out.String(), err)
	}
}

func TestRunLogsShowsTheErrorOfAJobThatFailedBeforeStarting(t *testing.T) {
	compiler := "install source entry: # pipeline\n./pipeline.go:91:26: cannot use *** as int value"
	reader := fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{Records: []controllerwire.LogRecord{jobLogRecord("r1", "ran")}, Complete: true}, nil
	}}
	runs := &statusReader{details: []controllerwire.RunDetail{{ID: "r1", Status: "failed", Jobs: []controllerwire.JobDetail{
		{ID: "build", Status: "done", Outcome: "failed", Error: compiler},
		{ID: "test", Status: "done", Outcome: "failed", Error: "step failed"},
	}, Attempts: []controllerwire.AttemptDetail{{JobID: "test", Attempt: 1, Generation: 1}}}}}
	for _, tc := range []struct {
		args []string
		want []string
		not  string
	}{
		{[]string{"-o", "plain"}, []string{"ran\n", "build failed before it started", compiler}, "step failed"},
		{[]string{"--job", "test", "-o", "plain"}, []string{"ran\n"}, "before it started"},
		{[]string{"-o", "json"}, []string{`"event":"job_failed_before_start"`, `"job_id":"build"`, "cannot use *** as int value"}, "step failed"},
	} {
		var out bytes.Buffer
		if err := observeRunLogs(t.Context(), logInvocation(t, append([]string{"logs", "--run", "r1"}, tc.args...)...), reader, runs, &out, &out, nil); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: missing %q in:\n%s", tc.args, want, &out)
			}
		}
		if strings.Contains(out.String(), tc.not) {
			t.Errorf("%v: unexpected %q in:\n%s", tc.args, tc.not, &out)
		}
	}
}

func TestRunLogsShowsTheErrorOfARunThatFailedToPlan(t *testing.T) {
	compiler := "install source entry: # pipeline\njobs/broken.go:6:9: undefined: qaDeliberateUndefined"
	reader := fakeLogReader{read: func(context.Context, string, controllerwire.LogQuery) (controllerwire.LogPage, error) {
		return controllerwire.LogPage{Complete: true}, nil
	}}
	detail := controllerwire.RunDetail{ID: "r1", Status: "failed", Failure: compiler, Jobs: []controllerwire.JobDetail{}}
	detail.Inputs.Pipeline = "hello"
	runs := &statusReader{details: []controllerwire.RunDetail{detail}}
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"-o", "plain"}, []string{"run error:\n" + compiler, "sparkwing pipeline plan --pipeline hello"}},
		{[]string{"--events-only", "-o", "plain"}, []string{compiler}},
		{[]string{"-o", "json"}, []string{`"event":"run_error"`, "undefined: qaDeliberateUndefined"}},
	} {
		var out bytes.Buffer
		if err := observeRunLogs(t.Context(), logInvocation(t, append([]string{"logs", "--run", "r1"}, tc.args...)...), reader, runs, &out, &out, nil); err != nil {
			t.Fatalf("%v: %v", tc.args, err)
		}
		for _, want := range tc.want {
			if !strings.Contains(out.String(), want) {
				t.Errorf("%v: missing %q in:\n%s", tc.args, want, &out)
			}
		}
	}
}
