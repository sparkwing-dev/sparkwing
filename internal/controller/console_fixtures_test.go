package controller

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engineid"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
)

var updateConsoleFixtures = flag.Bool("update-console-fixtures", false, "rewrite the console's wire fixtures from the Go types")

func consoleFixtures() map[string]any {
	started := time.Date(2026, 10, 28, 5, 1, 19, 0, time.UTC)
	finished := started.Add(33 * time.Second)
	inputs := runspec.Display{
		Pipeline: "hello", RequestedArgs: map[string]string{}, Args: map[string]string{"target": "prod"}, SecretArgs: []string{},
		Git:     runspec.Git{Repository: runspec.Repository{Key: "git:gitlab.com/acme/app", Display: "gitlab.com/acme/app"}, Ref: "main", SHA: "1b8e292f29420af1ae8e113e53faf3c05fb17249"},
		Lineage: runspec.Lineage{RetryOf: "run-earlier"},
	}
	summary := runstate.RunSummary{
		ID: "run-jobs", Number: 15, Pipeline: "hello", Status: "failed", TriggerSource: "github", GitBranch: "main",
		GitSHA: "1b8e292f29420af1ae8e113e53faf3c05fb17249", Args: map[string]string{"target": "prod"}, Error: "build failed",
		CreatedAt: started, StartedAt: started, FinishedAt: &finished, DeclaredRepo: "acme/app", RepoURL: "https://github.com/acme/app",
		GithubOwner: "acme", GithubRepo: "app", RetryOf: "run-earlier", TriggerUser: "octocat",
	}
	schedule := controllerwire.ScheduleView{
		ID: "sched-nightly", Name: "app/nightly", ScheduleName: "", RepoPath: "/src/app", Pipeline: "nightly", Where: "controller",
		Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip", Args: controllerwire.DisplayScheduleArgs(map[string]string{"target": "prod"}),
		Lock: controllerwire.LockView{State: "follows"}, Override: controllerwire.OverrideView{Fields: []string{}, Args: controllerwire.DisplayScheduleArgs(nil)},
		Effective: controllerwire.EffectiveView{Cron: "0 3 * * *", TZ: "UTC", Overlap: "skip", Args: controllerwire.DisplayScheduleArgs(map[string]string{"target": "prod"})},
		Declared:  true, State: "armed", ArmedAt: "2026-10-27T03:00:00Z", UpdatedAt: "2026-10-27T03:00:00Z",
		LastFiredAt: ptr("2026-10-28T03:00:00Z"), LastRunID: "run-jobs", LastOutcome: "fired", NextDueAt: ptr("2026-10-29T03:00:00Z"),
	}
	return map[string]any{
		"run-detail-plan-failure.json": controllerwire.RunDetail{
			ID: "run-plan", Number: 14, Team: "acme", Status: "failed", StateSeq: 3, Inputs: inputs,
			Failure: "install source entry: # pipeline\njobs/broken.go:6:9: undefined: qaDeliberateUndefined",
			Jobs:    []controllerwire.JobDetail{}, Attempts: []controllerwire.AttemptDetail{}, Notices: []engineid.Notice{}, FinishedAt: &finished,
		},
		"run-detail-jobs.json": controllerwire.RunDetail{
			ID: "run-jobs", Number: 15, Team: "acme", Status: "failed", StateSeq: 9, Inputs: inputs,
			Jobs: []controllerwire.JobDetail{
				{ID: "lint", Status: "done", Outcome: "success", Generation: 1, Expansion: "none", FinishedAt: &finished},
				{ID: "build", Needs: []string{"lint"}, Status: "done", Outcome: "failed", Error: "install source entry: ./pipeline.go:3:9: cannot use *** as int value", Generation: 1, Expansion: "none", FinishedAt: &finished},
				{ID: "test", Needs: []string{"lint"}, Status: "done", Outcome: "success", Generation: 1, Attempt: 1, StateSeq: 4, Expansion: "none", StartedAt: &started, FinishedAt: &finished, Annotations: []controllerwire.Annotation{{Level: "notice", Message: "42 tests"}}},
			},
			Attempts:  []controllerwire.AttemptDetail{{JobID: "test", Attempt: 1, Generation: 1, Outcome: "success", Executor: "runner", ExecutorName: "moonborn", Location: "local", StartedAt: &started, FinishedAt: &finished}},
			Notices:   []engineid.Notice{},
			StartedAt: &started, FinishedAt: &finished,
			Retry: &controllerwire.RetryDetail{Mode: "failed", Reused: []string{"lint"}},
		},
		"run-steps.json": controllerwire.RunSteps{Steps: []controllerwire.StepDetail{
			{JobID: "test", ID: "unit", Attempt: 1, Status: "passed", Outcome: "success", StartedAt: &started, FinishedAt: &finished, Annotations: []controllerwire.Annotation{{Level: "notice", Message: "42 tests"}}},
		}},
		"runs-list.json":   map[string]any{"runs": []runstate.RunSummary{summary}},
		"run-created.json": controllerwire.CreateRunResponse{RunID: "run-retry", Number: 16, Inputs: inputs, WriteScope: "team"},
		"runners.json": controllerwire.Runners{Runners: []controllerwire.RunnerDetail{{
			Name: "moonborn", Class: "runner", Location: "local", Labels: []string{"os=linux", "arch=amd64"}, ReservedPools: []string{}, Repos: []string{},
			LastSeen: "2026-10-28T05:01:19Z", Status: "busy", ActiveJobs: []controllerwire.RunnerJob{{RunID: "run-jobs", JobID: "test"}}, MaxConcurrent: 2,
			Capacity: controllerwire.RunnerResources{Cores: 8, MemoryBytes: 16 << 30},
			Headroom: &controllerwire.RunnerHeadroom{RunnerResources: controllerwire.RunnerResources{Cores: 6, MemoryBytes: 12 << 30}, QueueDepth: 0, ObservedAt: "2026-10-28T05:01:19Z"},
		}}},
		"pipelines.json": pipelinesList{Pipelines: []controllerwire.PipelineSummary{{Name: "hello", LastActivityAt: finished, LastRunID: "run-jobs", LastStatus: "failed", LastRunAt: &started, LastFinishedAt: &finished}}},
		"schedules.json": controllerwire.ScheduleOverview{
			Health:    controllerwire.HealthView{Schedules: 1, Armed: 1, Following: 1},
			Schedules: []controllerwire.ScheduleView{schedule},
			Upcoming:  []controllerwire.ScheduleUpcoming{{Schedule: "sched-nightly", Name: "app/nightly", At: "2026-10-29T03:00:00Z"}},
		},
		"schedule-detail.json": controllerwire.DetailView{
			Schedule: schedule,
			Fires: []controllerwire.FireView{{
				ID: "fire-1", ScheduleID: "sched-nightly", DueAt: "2026-10-28T03:00:00Z", DecidedAt: "2026-10-28T03:00:01Z", Outcome: "fired",
				RunID: "run-jobs", Args: controllerwire.DisplayScheduleArgs(map[string]string{"target": "prod"}), RunStatus: "failed",
			}},
			Upcoming: []string{"2026-10-29T03:00:00Z"},
		},
		"schedule-run.json": controllerwire.ScheduleRun{RunID: "run-scheduled", Schedule: schedule},
	}
}

func ptr[T any](v T) *T { return &v }

// TestConsoleFixturesMatchTheWireTypes keeps web/src/lib/testdata exactly what the controller encodes for the
// responses the console reads. The console's unit tests run its adapters over those files and its browser mocks
// serve them, so a renamed, added or removed field fails here until the fixtures are regenerated with
// -update-console-fixtures and the console follows.
func TestConsoleFixturesMatchTheWireTypes(t *testing.T) {
	for name, value := range consoleFixtures() {
		path := filepath.Join("..", "..", "web", "src", "lib", "testdata", name)
		want, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, '\n')
		if *updateConsoleFixtures {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from what the controller encodes; rerun with -update-console-fixtures and update the console", name)
		}
	}
}
