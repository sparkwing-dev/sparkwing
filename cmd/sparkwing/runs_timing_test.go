package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

var timingBase = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func at(seconds float64) *time.Time {
	t := timingBase.Add(time.Duration(seconds * float64(time.Second)))
	return &t
}

func timingEvent(seq int64, job, kind string, seconds float64) controllerwire.LogRecord {
	return controllerwire.LogRecord{RunID: "r1", Event: kind, JobID: job, Time: at(seconds), LineNo: seq}
}

func timingFixture() (controllerwire.RunDetail, []controllerwire.StepDetail, []controllerwire.LogRecord) {
	run := controllerwire.RunDetail{ID: "r1", Number: 7, Status: "failed", StartedAt: at(0), FinishedAt: at(80)}
	run.Inputs.Pipeline, run.Inputs.Git.SHA = "check", "abc"
	run.Jobs = []controllerwire.JobDetail{
		{ID: "build", Outcome: "success"},
		{ID: "test", Outcome: "failed", Reason: "sdk_error", Attempt: 2},
		{ID: "notify", Outcome: "cancelled", Reason: "user_cancelled", CancelRequestedAt: at(79)},
		{ID: "deploy", Outcome: "cancelled", Reason: "upstream_failed"},
	}
	run.Attempts = []controllerwire.AttemptDetail{{JobID: "build", Attempt: 1}, {JobID: "test", Attempt: 1}, {JobID: "test", Attempt: 2}}
	steps := []controllerwire.StepDetail{
		{JobID: "build", ID: "compile", Attempt: 1, Status: "passed", StartedAt: at(46), FinishedAt: at(50)},
		{JobID: "build", ID: "package", Attempt: 1, Status: "passed", StartedAt: at(51), FinishedAt: at(60)},
		{JobID: "test", ID: "lint", Attempt: 1, Status: "failed", StartedAt: at(63.5), FinishedAt: at(63.6)},
		{JobID: "test", ID: "unit", Attempt: 2, Status: "failed", StartedAt: at(64), FinishedAt: at(78)},
	}
	events := []controllerwire.LogRecord{
		timingEvent(1, "", "run_started", 0),
		timingEvent(2, "build", "job_ready", 1),
		timingEvent(3, "build", "job_claimed", 3),
		timingEvent(4, "build", "job_started", 43),
		timingEvent(5, "build", "job_succeeded", 62),
		timingEvent(6, "test", "job_ready", 62),
		timingEvent(7, "test", "job_claimed", 62.5),
		timingEvent(8, "test", "job_started", 63),
		timingEvent(9, "test", "job_failed", 79),
		timingEvent(10, "deploy", "job_cancelled", 79),
		timingEvent(11, "", "run_failed", 80),
	}
	return run, steps, events
}

func TestRunTimingPartitionsEveryJobAndTheWall(t *testing.T) {
	run, steps, events := timingFixture()
	got := measureRun(run, steps, events)
	if got.Phases.sum() != got.Wall || got.Wall != 80*time.Second {
		t.Fatalf("run phases %v do not sum to wall %v", got.Phases, got.Wall)
	}
	build := got.Jobs["build"]
	want := phaseDurations{phaseQueue: 2 * time.Second, phaseAdmit: 40 * time.Second, phaseSetup: 3 * time.Second, phaseRunning: 13 * time.Second, phaseGap: 3 * time.Second}
	if build.Phases != want || build.Total != 61*time.Second || build.Phases.sum() != build.Total {
		t.Fatalf("build phases %v total %v", build.Phases, build.Total)
	}
	test := got.Jobs["test"]
	if test.Phases.sum() != test.Total || test.Total != 17*time.Second || test.Attempts != 2 || test.Phases[phaseRunning] != 14100*time.Millisecond {
		t.Fatalf("test timing %+v", test)
	}
	if _, ok := got.Jobs["deploy"]; ok {
		t.Fatal("a job that never became ready has no timing")
	}
	if got.Phases[phaseIdle] != 2*time.Second || got.Phases[phaseAdmit] != 40*time.Second+500*time.Millisecond {
		t.Fatalf("run phases %v", got.Phases)
	}
	if len(got.Failures) != 1 || got.Failures[0] != (timingFailureKey{Job: "test", Step: "unit", Outcome: "failed", Reason: "sdk_error"}) {
		t.Fatalf("failures %+v", got.Failures)
	}
}

func TestRunsTimingReadsFinishedRunsAndEmitsNDJSON(t *testing.T) {
	run, steps, events := timingFixture()
	live := controllerwire.RunDetail{ID: "r2", Number: 8, Status: "running", StartedAt: at(0)}
	second := run
	second.ID, second.Number, second.Status = "r0", 6, "success"
	pages := 0
	reader := listingReader{
		list: func(_ context.Context, q controllerwire.RunQuery) (controllerwire.RunPage, error) {
			pages++
			if q.Pipeline != "check" || !q.RootOnly {
				t.Fatalf("query %+v", q)
			}
			if q.Cursor == "" {
				return controllerwire.RunPage{Runs: []controllerwire.RunDetail{live, run}, Truncated: true, NextCursor: "next"}, nil
			}
			return controllerwire.RunPage{Runs: []controllerwire.RunDetail{second}, Truncated: true, NextCursor: "after"}, nil
		},
		steps: func(context.Context, string) (controllerwire.RunSteps, error) {
			return controllerwire.RunSteps{Steps: steps}, nil
		},
	}
	reads := map[string]int{}
	logs := fakeLogReader{read: func(_ context.Context, id string, q controllerwire.LogQuery) (controllerwire.LogPage, error) {
		if !q.EventsOnly {
			t.Fatalf("log query %+v", q)
		}
		reads[id]++
		page := make([]controllerwire.LogRecord, len(events))
		for i, event := range events {
			event.RunID = id
			page[i] = event
		}
		if q.Cursor == "" {
			return controllerwire.LogPage{Records: page[:5], Truncated: true, NextCursor: "more"}, nil
		}
		return controllerwire.LogPage{Records: page[5:], Complete: true, NextCursor: "end"}, nil
	}}
	inv, err := clisurface.Parse("sparkwing", []string{"runs", "timing", "--pipeline", "check", "--last", "2", "-o", "json"})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := observeRunsTiming(t.Context(), inv, reader, logs, &out); err != nil {
		t.Fatal(err)
	}
	if pages != 2 || reads["r1"] != 2 || reads["r0"] != 2 || reads["r2"] != 0 {
		t.Fatalf("pages=%d reads=%v", pages, reads)
	}
	kinds := map[string]int{}
	var summary timingSummaryRecord
	scanner := bufio.NewScanner(&out)
	for scanner.Scan() {
		var record struct{ Kind string }
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		kinds[record.Kind]++
		if record.Kind == "summary" {
			if err := json.Unmarshal(scanner.Bytes(), &summary); err != nil {
				t.Fatal(err)
			}
		}
	}
	if kinds["run"] != 2 || kinds["job"] != 2 || kinds["failure"] != 1 || kinds["summary"] != 1 || kinds["page"] != 1 {
		t.Fatalf("records %v", kinds)
	}
	if summary.Runs != 2 || summary.Success != 1 || summary.Failed != 1 || summary.RepeatedCommits != 1 || summary.Admit.P50 != 40*time.Second+500*time.Millisecond {
		t.Fatalf("summary %+v", summary)
	}
}

func TestRunsTimingTextNamesItsPhases(t *testing.T) {
	run, steps, events := timingFixture()
	var out bytes.Buffer
	if err := writeRunsTiming(&out, "check", nil, 20, []runTiming{measureRun(run, steps, events)}, "pretty"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"check: 1 finished runs", "admit", "idle", "build", "Longest waits:", "#7", "Failed jobs", "sdk_error"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in\n%s", want, out.String())
		}
	}
}

func TestRunsTimingRequiresAPipeline(t *testing.T) {
	if _, err := clisurface.Parse("sparkwing", []string{"runs", "timing"}); err == nil {
		t.Fatal("timing without --pipeline parsed")
	}
}

func TestRunTimingMovesRunnerClockStepsIntoTheJobWindow(t *testing.T) {
	run, steps, events := timingFixture()
	skewed := make([]controllerwire.StepDetail, len(steps))
	for i, step := range steps {
		skewed[i] = step
		if step.JobID == "build" {
			skewed[i].StartedAt, skewed[i].FinishedAt = at(step.StartedAt.Sub(timingBase).Seconds()-10), at(step.FinishedAt.Sub(timingBase).Seconds()-10)
		}
	}
	got := measureRun(run, skewed, events)
	build := got.Jobs["build"]
	if got.StepShift != 7*time.Second || build.Phases[phaseRunning] != 13*time.Second || build.Phases.sum() != build.Total {
		t.Fatalf("shift %v build %+v", got.StepShift, build)
	}
}

func TestRunsTimingJobFilterBoundsTheSections(t *testing.T) {
	run, steps, events := timingFixture()
	var out bytes.Buffer
	if err := writeRunsTiming(&out, "check", []string{"build"}, 20, []runTiming{measureRun(run, steps, events)}, "pretty"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sdk_error") || !strings.Contains(out.String(), "build") {
		t.Fatalf("filtered output:\n%s", out.String())
	}
}

func TestRunTimingAlignsEachAttemptToItsOwnStart(t *testing.T) {
	run := controllerwire.RunDetail{ID: "r1", Number: 1, Status: "success", StartedAt: at(0), FinishedAt: at(130)}
	run.Jobs = []controllerwire.JobDetail{{ID: "build", Outcome: "success", Attempt: 2}}
	steps := []controllerwire.StepDetail{
		{JobID: "build", ID: "a", Attempt: 1, Status: "failed", StartedAt: at(-8), FinishedAt: at(-5)},
		{JobID: "build", ID: "a", Attempt: 2, Status: "passed", StartedAt: at(111), FinishedAt: at(119)},
	}
	events := []controllerwire.LogRecord{
		timingEvent(1, "build", "job_ready", 0), timingEvent(2, "build", "job_claimed", 0), timingEvent(3, "build", "job_started", 0),
		timingEvent(4, "build", "job_retrying", 100), timingEvent(5, "build", "job_succeeded", 120),
	}
	got := measureRun(run, steps, events)
	if build := got.Jobs["build"]; build.Phases[phaseRunning] != 11*time.Second || got.StepShift != 8*time.Second {
		t.Fatalf("build %+v shift %v", build, got.StepShift)
	}
}

func TestRunsTimingTopRaisesTheBound(t *testing.T) {
	run, steps, events := timingFixture()
	timings := []runTiming{measureRun(run, steps, events)}
	var out bytes.Buffer
	if err := writeRunsTiming(&out, "check", nil, 1, timings, "pretty"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 more jobs: raise --top") {
		t.Fatalf("bounded output:\n%s", out.String())
	}
	out.Reset()
	if err := writeRunsTiming(&out, "check", nil, 2, timings, "json"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"jobs":2,"jobs_returned":2`) || !strings.Contains(out.String(), `"truncated":false`) {
		t.Fatalf("json page:\n%s", out.String())
	}
}
