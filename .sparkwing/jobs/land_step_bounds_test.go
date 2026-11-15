package jobs

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwingtest"
)

func TestLandRefusesStepBoundsThatKeepThePush(t *testing.T) {
	for _, tc := range []struct {
		name, start, stop string
		queued            bool
		wants             []string
	}{
		{name: "start at build", start: "build", wants: []string{"--sw-start-at build", "upstream of build"}},
		{name: "stop at changelog", stop: "changelog", wants: []string{"--sw-stop-at changelog", "downstream of changelog"}},
		{name: "start at push skips verify", start: "push", wants: []string{"--sw-start-at push", "upstream of push"}},
		{name: "historical budget start", start: "budget", wants: []string{"--sw-start-at budget"}},
		{name: "bounded window", start: "build", stop: "lint", wants: []string{"--sw-start-at build --sw-stop-at lint", "outside"}},
		{name: "queued start at build", start: "build", queued: true, wants: []string{"--sw-start-at build"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := sparkwing.RunContext{StartAt: tc.start, StopAt: tc.stop}
			_, err := newTestHost(t, sparkwingtest.Options{Run: run}).Plan(t.Context(), "Land", landBoundArgs(tc.queued))
			if err == nil {
				t.Fatalf("Land planned a pushing run with --sw-start-at=%q --sw-stop-at=%q", tc.start, tc.stop)
			}
			for _, want := range append(tc.wants, "--sw-stop-at verify", "--sw-dry-run") {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestLandKeepsStepBoundsThatCannotPush(t *testing.T) {
	for _, tc := range []struct {
		name   string
		run    sparkwing.RunContext
		queued bool
	}{
		{name: "unbounded", run: sparkwing.RunContext{}},
		{name: "stop at verify", run: sparkwing.RunContext{StopAt: "verify"}},
		{name: "stop at verify after a start bound", run: sparkwing.RunContext{StartAt: "build", StopAt: "verify"}},
		{name: "dry run start at build", run: sparkwing.RunContext{StartAt: "build", DryRun: true}},
		{name: "dry run stop at changelog", run: sparkwing.RunContext{StopAt: "changelog", DryRun: true}},
		{name: "queued stop at verify", run: sparkwing.RunContext{StopAt: "verify"}, queued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := newTestHost(t, sparkwingtest.Options{Run: tc.run}).Plan(t.Context(), "Land", landBoundArgs(tc.queued))
			if err != nil {
				t.Fatalf("Land refused a selection that cannot push: %v", err)
			}
			var steps []string
			for _, job := range doc.Jobs {
				if job.ID == "land" {
					for _, step := range job.Steps {
						steps = append(steps, step.ID)
					}
				}
			}
			if strings.Join(steps, ",") != "verify,push,record" {
				t.Fatalf("land job steps = %v, want verify, push and record", steps)
			}
		})
	}
}

func landBoundArgs(queued bool) map[string]string {
	if !queued {
		return nil
	}
	return map[string]string{"queued": "true", "queued-main": strings.Repeat("a", 40), "brief": "bounded queued land"}
}
