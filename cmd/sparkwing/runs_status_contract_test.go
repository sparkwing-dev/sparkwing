package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
)

type statusReader struct {
	controllerwire.RunReader
	details   []controllerwire.RunDetail
	selectors []string
	err       error
	steps     controllerwire.RunSteps
	receipt   controllerwire.Receipt
	pages     map[string]controllerwire.RunPage
	queries   []controllerwire.RunQuery
}

func (r *statusReader) ReadRun(_ context.Context, id string) (controllerwire.RunDetail, error) {
	r.selectors = append(r.selectors, id)
	if r.err != nil {
		return controllerwire.RunDetail{}, r.err
	}
	d := r.details[0]
	if len(r.details) > 1 {
		r.details = r.details[1:]
	}
	return d, nil
}

func (r *statusReader) ReadSteps(context.Context, string) (controllerwire.RunSteps, error) {
	return r.steps, r.err
}

func (r *statusReader) ReadReceipt(context.Context, string) (controllerwire.Receipt, error) {
	return r.receipt, r.err
}

func (r *statusReader) ListRuns(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
	r.queries = append(r.queries, q)
	return r.pages[q.ParentRunID+"/"+q.Cursor], r.err
}

func statusInvocation(t *testing.T, args ...string) clisurface.Invocation {
	t.Helper()
	base := []string{"runs", "status", "--run", "412"}
	if !strings.Contains(strings.Join(args, " "), "-o ") {
		base = append(base, "-o", "json")
	}
	inv, err := clisurface.Parse("sparkwing", append(base, args...))
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestRunStatusUsesAuthoritativeDetailForEveryConnection(t *testing.T) {
	want := controllerwire.RunDetail{
		ID: "r1", Number: 412, Status: "success", StateSeq: 9,
		Inputs:   runspec.Display{Pipeline: "check", Args: map[string]string{"password": "***"}},
		Jobs:     []controllerwire.JobDetail{{ID: "build", Status: "finished", Outcome: "memoized", Generation: 3, Attempt: 2}},
		Attempts: []controllerwire.AttemptDetail{{JobID: "build", Attempt: 2, Generation: 3, Outcome: "memoized", Executor: "runner-1"}},
		Notices:  []engineid.Notice{{Kind: engineid.NoticeSupportEnding, Reason: "support ends soon", Hint: "sparkwing upgrade"}},
	}
	for _, profile := range []string{"", "prod"} {
		args := []string{}
		if profile != "" {
			args = append(args, "--profile", profile)
		}
		r := &statusReader{details: []controllerwire.RunDetail{want}}
		var out bytes.Buffer
		if err := observeRunStatus(t.Context(), statusInvocation(t, args...), r, &out, nil); err != nil {
			t.Fatal(err)
		}
		var got controllerwire.RunDetail
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("detail=%s err=%v", &out, err)
		}
		if !reflect.DeepEqual(r.selectors, []string{"412"}) {
			t.Fatalf("selector rewritten or detail reread: %v", r.selectors)
		}
	}
}

func TestRunStatusFollowEmitsChangedStatesAndFinalOutcome(t *testing.T) {
	r := &statusReader{details: []controllerwire.RunDetail{
		{ID: "r1", Status: "running", StateSeq: 1}, {ID: "r1", Status: "running", StateSeq: 1}, {ID: "r1", Status: "failed", StateSeq: 2},
	}}
	waits := 0
	wait := func(context.Context, time.Duration) error { waits++; return nil }
	var out bytes.Buffer
	err := observeRunStatus(t.Context(), statusInvocation(t, "--follow"), r, &out, wait)
	if exitCodeFor(err) != 1 || waits != 2 || strings.Count(out.String(), "\n") != 2 {
		t.Fatalf("err=%v waits=%d out=%s", err, waits, &out)
	}
}

func TestRunStatusIncompleteObservationsExitThree(t *testing.T) {
	for _, failure := range []error{context.Canceled, context.DeadlineExceeded, &apierr.Error{Code: apierr.Unavailable, Message: "controller restarting"}} {
		r := &statusReader{err: failure}
		err := observeRunStatus(t.Context(), statusInvocation(t, "--exit-zero"), r, io.Discard, nil)
		if exitCodeFor(err) != 3 || !errors.Is(err, failure) {
			t.Fatalf("lookup: %v", err)
		}
	}
	r := &statusReader{details: []controllerwire.RunDetail{{ID: "r1", Status: "running"}}}
	err := observeRunStatus(t.Context(), statusInvocation(t, "--follow", "--timeout", "1s"), r, io.Discard, func(context.Context, time.Duration) error { return context.DeadlineExceeded })
	if exitCodeFor(err) != 3 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
	err = observeRunStatus(t.Context(), statusInvocation(t), r, io.Discard, nil)
	if exitCodeFor(err) != 3 {
		t.Fatalf("nonterminal: %v", err)
	}
}

func TestRunStatusValidatesBeforeConnecting(t *testing.T) {
	for _, args := range [][]string{{"--timeout", "1s"}, {"--view", "unknown"}, {"--poll", "0s"}, {"--width", "0"}} {
		_, err := parseRunStatus(statusInvocation(t, args...))
		if exitCodeFor(err) != 2 {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestRunStatusViewsUseSafeReadModels(t *testing.T) {
	detail := controllerwire.RunDetail{ID: "r1", Status: "success", Inputs: runspec.Display{Pipeline: "check"}, Jobs: []controllerwire.JobDetail{{ID: "build", Status: "finished", Outcome: "failed", StatusDetail: "masked failure"}}}
	r := &statusReader{details: []controllerwire.RunDetail{detail}, steps: controllerwire.RunSteps{Steps: []controllerwire.StepDetail{{JobID: "build", ID: "compile", Outcome: "failed"}}}, receipt: controllerwire.Receipt{RunID: "r1", ReceiptSHA: "sha256:authoritative", Cost: controllerwire.ReceiptCost{Currency: "USD", ComputeCents: 17, Settled: true}}}
	for _, view := range []string{"summary", "errors", "timeline", "receipt"} {
		var out bytes.Buffer
		if err := observeRunStatus(t.Context(), statusInvocation(t, "--view", view, "--steps"), r, &out, nil); err != nil {
			t.Fatal(err)
		}
		if !json.Valid(bytes.TrimSpace(out.Bytes())) {
			t.Fatalf("%s: %s", view, &out)
		}
		if view == "receipt" && !strings.Contains(out.String(), "sha256:authoritative") {
			t.Fatalf("receipt rebuilt: %s", &out)
		}
	}
	for _, format := range []string{"plain", "pretty"} {
		var out bytes.Buffer
		if err := observeRunStatus(t.Context(), statusInvocation(t, "-o", format, "--steps"), r, &out, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "compile") || strings.Contains(out.String(), "node") {
			t.Fatalf("step view: %s", &out)
		}
	}
}

func TestRunStatusTreePaginatesAndRejectsCycles(t *testing.T) {
	r := &statusReader{details: []controllerwire.RunDetail{{ID: "root", Status: "success"}}, pages: map[string]controllerwire.RunPage{
		"root/":     {Runs: []controllerwire.RunDetail{{ID: "child-a", Status: "success"}}, NextCursor: "next", Truncated: true},
		"root/next": {Runs: []controllerwire.RunDetail{{ID: "child-b", Status: "failed"}}},
	}}
	var out bytes.Buffer
	if err := observeRunStatus(t.Context(), statusInvocation(t, "--view", "tree"), r, &out, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "child-b") || len(r.queries) != 4 {
		t.Fatalf("tree=%s queries=%v", &out, r.queries)
	}
	r.pages["child-a/"] = controllerwire.RunPage{Runs: []controllerwire.RunDetail{{ID: "root"}}}
	if err := observeRunStatus(t.Context(), statusInvocation(t, "--view", "tree"), r, io.Discard, nil); err == nil {
		t.Fatal("cyclic tree accepted")
	}
}

func TestRunStatusFollowsDescendantsWithoutParentTransitions(t *testing.T) {
	r := &statusReader{details: []controllerwire.RunDetail{{ID: "root", Status: "running"}}, pages: map[string]controllerwire.RunPage{
		"root/": {Runs: []controllerwire.RunDetail{{ID: "child", Status: "running"}}},
	}}
	var out bytes.Buffer
	waits := 0
	err := observeRunStatus(t.Context(), statusInvocation(t, "--follow", "--view", "tree"), r, &out, func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			return context.Canceled
		}
		r.pages["root/"] = controllerwire.RunPage{Runs: []controllerwire.RunDetail{{ID: "child", Status: "success"}}}
		return nil
	})
	if exitCodeFor(err) != 3 || strings.Count(out.String(), "\n") != 2 || !strings.Contains(out.String(), `"status":"success"`) {
		t.Fatalf("tree observation=%s err=%v", &out, err)
	}
}

func TestRunStatusFollowsReadTimeNotices(t *testing.T) {
	r := &statusReader{details: []controllerwire.RunDetail{
		{ID: "r1", Status: "running", StateSeq: 4},
		{ID: "r1", Status: "running", StateSeq: 4, Notices: []engineid.Notice{{Kind: engineid.NoticeEnforcement, Reason: "policy changed"}}},
	}}
	var out bytes.Buffer
	waits := 0
	err := observeRunStatus(t.Context(), statusInvocation(t, "--follow"), r, &out, func(context.Context, time.Duration) error {
		waits++
		if waits == 2 {
			return context.Canceled
		}
		return nil
	})
	if exitCodeFor(err) != 3 || !strings.Contains(out.String(), "policy changed") {
		t.Fatalf("notice lost: %s %v", &out, err)
	}
}

func TestRunStatusReceiptMismatchIsIncomplete(t *testing.T) {
	r := &statusReader{details: []controllerwire.RunDetail{{ID: "r1", Status: "success"}}, receipt: controllerwire.Receipt{RunID: "other"}}
	var out bytes.Buffer
	err := observeRunStatus(t.Context(), statusInvocation(t, "--view", "receipt", "--exit-zero"), r, &out, nil)
	if exitCodeFor(err) != 3 || out.Len() != 0 {
		t.Fatalf("receipt mismatch: %s %v", &out, err)
	}
}

func TestRunStatusPlainValuesAndTerminalExits(t *testing.T) {
	for _, status := range []string{"success", "failed", "cancelled", "running"} {
		for _, exitZero := range []bool{false, true} {
			r := &statusReader{details: []controllerwire.RunDetail{{ID: "r1", Status: status}}}
			args := []string{"-o", "plain"}
			if exitZero {
				args = append(args, "--exit-zero")
			}
			var out bytes.Buffer
			err := observeRunStatus(t.Context(), statusInvocation(t, args...), r, &out, nil)
			want := 1
			if exitZero || status == "success" {
				want = 0
			}
			if status == "running" {
				want = 3
			}
			if out.String() != status+"\n" || exitCodeFor(err) != want {
				t.Fatalf("%s exit-zero=%t: %q %v", status, exitZero, out.String(), err)
			}
		}
	}
}

func TestRunStatusJSONIsTheOnlyDocumentForAnUnsuccessfulRun(t *testing.T) {
	for _, status := range []string{"failed", "running"} {
		r := &statusReader{details: []controllerwire.RunDetail{{ID: "r1", Status: status}}}
		var out bytes.Buffer
		err := observeRunStatus(t.Context(), statusInvocation(t), r, &out, nil)
		writeFailure(&out, &invocationError{inv: statusInvocation(t), cause: err})
		decoder := json.NewDecoder(&out)
		documents := 0
		for decoder.More() {
			var document map[string]any
			if err := decoder.Decode(&document); err != nil {
				t.Fatal(err)
			}
			documents++
		}
		if documents != 1 || exitCodeFor(err) == 0 {
			t.Fatalf("%s: %d documents on stdout and stderr, exit %d", status, documents, exitCodeFor(err))
		}
	}
}
