package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
)

func TestRunStatusExplainsEveryJobAndFailingStep(t *testing.T) {
	for _, format := range []string{"plain", "pretty", "json"} {
		t.Run(format, func(t *testing.T) {
			reader := &statusReader{
				details: []controllerwire.RunDetail{{ID: "r1", Status: "failed", Jobs: []controllerwire.JobDetail{
					{ID: "configure", Status: "done", Outcome: "success"},
					{ID: "weather-check", Status: "done", Outcome: "failed", StatusDetail: "weather-report failed"},
					{ID: "error-canary", Status: "done", Outcome: "failed"},
				}}},
				steps: controllerwire.RunSteps{Steps: []controllerwire.StepDetail{
					{JobID: "weather-check", ID: "run", Status: "done", Outcome: "failed", Reason: "service request fields cannot be null"},
					{JobID: "error-canary", ID: "shard-alpha", Status: "done", Outcome: "failed", Reason: "demo failure"},
				}},
			}
			var out bytes.Buffer
			err := observeRunStatus(t.Context(), statusInvocation(t, "-o", format), reader, &out, nil)
			if exitCodeFor(err) != 1 {
				t.Fatalf("status exit: %v", err)
			}
			for _, want := range []string{"configure", "success", "weather-check", "weather-report failed", "error-canary", "run", "service request fields cannot be null", "shard-alpha", "demo failure"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in status:\n%s", want, &out)
				}
			}
		})
	}
}

func TestRunStatusShowsOptionalFailureWithoutFailingSuccessfulRun(t *testing.T) {
	reader := &statusReader{
		details: []controllerwire.RunDetail{{ID: "r1", Status: "success", Jobs: []controllerwire.JobDetail{{ID: "error-canary", Status: "done", Outcome: "failed"}}}},
		steps: controllerwire.RunSteps{Steps: []controllerwire.StepDetail{
			{JobID: "error-canary", ID: "passed", Outcome: "success"},
			{JobID: "error-canary", ID: "shard-alpha", Outcome: "failed", Reason: "demo failure"},
		}},
	}
	for _, allSteps := range []bool{false, true} {
		args := []string{"-o", "plain"}
		if allSteps {
			args = append(args, "--steps")
		}
		var out bytes.Buffer
		if err := observeRunStatus(t.Context(), statusInvocation(t, args...), reader, &out, nil); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.String(), "success\n") || !strings.Contains(out.String(), "error-canary/shard-alpha") || !strings.Contains(out.String(), "demo failure") || strings.Contains(out.String(), "error-canary/passed") != allSteps {
			t.Fatalf("all steps=%t: %s", allSteps, &out)
		}
	}
}

func TestRunStatusShowsAnUnstartedJobsError(t *testing.T) {
	compiler := "install source entry: # pipeline\n./pipeline.go:91:26: cannot use *** as int value"
	for _, args := range [][]string{{"-o", "plain"}, {"-o", "pretty"}, {"-o", "json"}, {"--view", "errors", "-o", "pretty"}} {
		reader := &statusReader{details: []controllerwire.RunDetail{{ID: "r1", Status: "failed", Jobs: []controllerwire.JobDetail{{ID: "build", Status: "done", Outcome: "failed", Error: compiler}}}}}
		var out bytes.Buffer
		if err := observeRunStatus(t.Context(), statusInvocation(t, args...), reader, &out, nil); exitCodeFor(err) != 1 {
			t.Fatalf("%v status exit: %v", args, err)
		}
		want := compiler
		if args[1] == "json" {
			want = "cannot use *** as int value"
		}
		if !strings.Contains(out.String(), want) {
			t.Errorf("%v: missing the job's error in:\n%s", args, &out)
		}
	}
}

func TestRunStatusShowsTheErrorOfARunThatFailedToPlan(t *testing.T) {
	compiler := "install source entry: # pipeline\njobs/broken.go:6:9: undefined: qaDeliberateUndefined"
	for _, args := range [][]string{{"-o", "plain"}, {"-o", "pretty"}, {"-o", "json"}, {"--view", "errors", "-o", "pretty"}, {"--view", "errors", "-o", "json"}} {
		detail := controllerwire.RunDetail{ID: "r1", Status: "failed", Failure: compiler, Jobs: []controllerwire.JobDetail{}}
		detail.Inputs.Pipeline = "hello"
		reader := &statusReader{details: []controllerwire.RunDetail{detail}}
		var out bytes.Buffer
		if err := observeRunStatus(t.Context(), statusInvocation(t, args...), reader, &out, nil); exitCodeFor(err) != 1 {
			t.Fatalf("%v status exit: %v", args, err)
		}
		want := []string{compiler}
		if args[len(args)-1] == "json" {
			want = []string{"undefined: qaDeliberateUndefined", `"hint":"the pipeline failed to plan`, "sparkwing pipeline plan --pipeline hello"}
		} else if args[0] != "--view" {
			want = append(want, "sparkwing pipeline plan --pipeline hello")
		}
		for _, text := range want {
			if !strings.Contains(out.String(), text) {
				t.Errorf("%v: missing %q in:\n%s", args, text, &out)
			}
		}
	}
}
