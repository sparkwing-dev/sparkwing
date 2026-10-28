package main

import (
	"bytes"
	"context"
	"errors"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

type fakeRunReview struct {
	run                 controllerwire.RunDetail
	steps               controllerwire.RunSteps
	approvals           controllerwire.Approvals
	readErr, errorWrite error
	selectors           []string
	stepReads           int
	annotation          controllerwire.AnnotationWrite
	decision            controllerwire.ApprovalDecision
}

func (f *fakeRunReview) ReadRun(_ context.Context, id string) (controllerwire.RunDetail, error) {
	f.selectors = append(f.selectors, id)
	return f.run, f.readErr
}

func (f *fakeRunReview) ReadSteps(_ context.Context, id string) (controllerwire.RunSteps, error) {
	f.selectors = append(f.selectors, id)
	f.stepReads++
	return f.steps, f.readErr
}

func (f *fakeRunReview) ListApprovals(_ context.Context, id string) (controllerwire.Approvals, error) {
	f.selectors = append(f.selectors, id)
	return f.approvals, f.readErr
}

func (f *fakeRunReview) AppendAnnotation(_ context.Context, run, job string, input controllerwire.AnnotationWrite) error {
	f.selectors = append(f.selectors, run, job)
	f.annotation = input
	return f.errorWrite
}

func (f *fakeRunReview) ResolveApproval(_ context.Context, run, job string, input controllerwire.ApprovalDecision) (controllerwire.ApprovalResult, error) {
	f.selectors = append(f.selectors, run, job)
	f.decision = input
	resolved := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	return controllerwire.ApprovalResult{Approval: controllerwire.Approval{RunID: "run-stable", JobID: job, Resolution: input.Resolution, Approver: "server-actor", ResolvedAt: &resolved}, Replayed: true}, f.errorWrite
}

func reviewFixture() *fakeRunReview {
	return &fakeRunReview{run: controllerwire.RunDetail{ID: "run-stable", Jobs: []controllerwire.JobDetail{
		{ID: "build", Annotations: []nodeproto.Annotation{{Level: "info", Message: "first"}, {Level: "warn", Message: "second", File: "main.go", Line: 7, Column: 3, EndLine: 8}}},
		{ID: "deploy"},
	}}, steps: controllerwire.RunSteps{Steps: []controllerwire.StepDetail{{JobID: "deploy", ID: "canary", Annotations: []nodeproto.Annotation{{Level: "error", Message: "third", File: "deploy.ts", Line: 5}}}}}}
}

func runReviewFake(t *testing.T, f *fakeRunReview, args []string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runReviewCommandArgs(t.Context(), args, &out, func(context.Context, string) (controllerwire.RunReview, func(), error) { return f, func() {}, nil })
	return out.String(), err
}

func TestReviewAnnotationsFiltersAndLocations(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		count int
		want  string
		steps int
	}{
		{"job default", nil, 2, "second", 0},
		{"include steps", []string{"--steps"}, 3, "third", 1},
		{"job", []string{"--job", "deploy", "--steps"}, 1, "third", 1},
		{"step", []string{"--step", "canary"}, 1, "third", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := reviewFixture()
			args := append([]string{"runs", "annotations", "list", "--run", "412", "-o", "json"}, tc.flags...)
			out, err := runReviewFake(t, f, args)
			if err != nil {
				t.Fatal(err)
			}
			got := decodeNDJSON[annotationEntry](t, out)
			if len(got) != tc.count || !strings.Contains(out, tc.want) || f.stepReads != tc.steps {
				t.Fatalf("output=%s calls=%+v", out, f)
			}
			if tc.count > 1 && (got[0].Message != "first" || got[1].Message != "second") {
				t.Fatalf("annotation order changed: %+v", got)
			}
			if f.selectors[0] != "412" || strings.Contains(out, "node_id") {
				t.Fatalf("selector/vocabulary: %s %+v", out, f.selectors)
			}
			for _, entry := range got {
				if entry.Message == "second" && (entry.Level != "warn" || entry.File != "main.go" || entry.Line != 7 || entry.Column != 3 || entry.EndLine != 8) {
					t.Fatalf("location lost: %+v", entry)
				}
			}
		})
	}
}

func TestReviewAnnotationsMissingAndEmptyDiffer(t *testing.T) {
	f := reviewFixture()
	f.run.Jobs = nil
	out, err := runReviewFake(t, f, []string{"runs", "annotations", "list", "--run", "412", "-o", "json"})
	if err != nil || out != "" {
		t.Fatalf("empty run: %q %v", out, err)
	}
	f.readErr = &apierr.Error{Code: apierr.NotFound, Message: "run missing"}
	out, err = runReviewFake(t, f, []string{"runs", "annotations", "list", "--run", "missing", "--steps", "-o", "json"})
	if err == nil || out != "" || f.stepReads != 0 {
		t.Fatalf("missing run: %q %v", out, err)
	}
}

func TestReviewAnnotationWriteAndPolicyGap(t *testing.T) {
	f := reviewFixture()
	out, err := runReviewFake(t, f, []string{"runs", "annotations", "add", "--run", "412", "--job", "deploy", "--step", "canary", "--message", "secret raw text", "-o", "json"})
	if err != nil || f.annotation.StepID != "canary" || f.annotation.Level != "info" || f.annotation.Message != "secret raw text" || strings.Contains(out, `"applied"`) || !strings.Contains(out, `"step_id":"canary"`) || strings.Contains(out, "secret raw text") {
		t.Fatalf("write output=%s input=%+v err=%v", out, f.annotation, err)
	}
	var api *apierr.Error
	_, err = runReviewCommandWithoutTransport(t, []string{"runs", "annotations", "add", "--run", "412", "--message", "note"})
	if !errors.As(err, &api) || api.Code != apierr.PolicyMissing {
		t.Fatalf("run annotation gap: %v", err)
	}
	f.errorWrite = &apierr.Error{Code: apierr.Forbidden, Message: "member cannot report"}
	out, err = runReviewFake(t, f, []string{"runs", "annotations", "add", "--run", "412", "--job", "deploy", "--message", "note", "-o", "plain"})
	if !errors.As(err, &api) || api.Code != apierr.Forbidden || out != "" {
		t.Fatalf("forbidden write: %q %v", out, err)
	}
}

func runReviewCommandWithoutTransport(t *testing.T, args []string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runReviewCommandArgs(t.Context(), args, &out, func(context.Context, string) (controllerwire.RunReview, func(), error) {
		t.Fatal("unexpected controller resolution")
		return nil, nil, nil
	})
	return out.String(), err
}

func TestReviewGrammarBeforeController(t *testing.T) {
	for _, args := range [][]string{
		{"runs", "annotations", "list"},
		{"runs", "annotations", "list", "--run", "r", "--node", "j"},
		{"runs", "annotations", "add", "--run", "r", "--job", "j", "-m", "text"},
		{"runs", "approvals", "approve", "--run", "r"},
		{"runs", "approvals", "deny", "r", "j"},
	} {
		if _, err := runReviewCommandWithoutTransport(t, args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	if _, err := runReviewCommandWithoutTransport(t, []string{"runs", "annotations", "list", "--help"}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewAnnotationsPlainIsOneValuePerLine(t *testing.T) {
	f := reviewFixture()
	f.run.Jobs[0].Annotations = []nodeproto.Annotation{{Level: "info", Message: "first\nsecond\rline"}}
	out, err := runReviewFake(t, f, []string{"runs", "annotations", "list", "--run", "412", "-o", "plain"})
	if err != nil || out != "first second line\n" {
		t.Fatalf("plain annotation=%q err=%v", out, err)
	}
	out, err = runReviewFake(t, f, []string{"runs", "annotations", "add", "--run", "412", "--job", "build", "--step", "compile", "--message", "note", "-o", "plain"})
	if err != nil || out != "412/build/compile\n" {
		t.Fatalf("plain mutation=%q err=%v", out, err)
	}
}

func TestReviewProfileSelectionAndTransportCleanup(t *testing.T) {
	for _, profile := range []string{"", "remote"} {
		f := reviewFixture()
		args := []string{"runs", "annotations", "list", "--run", "412", "-o", "json"}
		if profile != "" {
			args = append([]string{"--profile", profile}, args...)
		}
		closed := false
		var out bytes.Buffer
		err := runReviewCommandArgs(t.Context(), args, &out, func(_ context.Context, selected string) (controllerwire.RunReview, func(), error) {
			if selected != profile {
				t.Fatalf("selected profile %q, want %q", selected, profile)
			}
			return f, func() { closed = true }, nil
		})
		if err != nil || !closed || f.selectors[0] != "412" {
			t.Fatalf("profile=%q closed=%v err=%v", profile, closed, err)
		}
	}
}
