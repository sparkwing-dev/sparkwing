package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

func TestTriggerLinesDescribeThePipelineOnly(t *testing.T) {
	lines := describeTriggers(pipelines.Triggers{
		PullRequest: &pipelines.PullRequestTrigger{},
		Push:        &pipelines.PushTrigger{},
	})
	if len(lines) != 2 {
		t.Fatalf("got %d trigger lines, want 2", len(lines))
	}
	for _, l := range lines {
		blob := strings.ToLower(l.Event + " " + l.Detail + " " + l.Advisory)
		for _, universal := range []string{"webhook", "not yet live", "controller", "install"} {
			if strings.Contains(blob, universal) {
				t.Errorf("%s mentions %q, which is true of every such trigger and says nothing about this pipeline: %+v",
					l.Event, universal, l)
			}
		}
	}
}

func TestUnenforcedFiltersAreMarkedAdvisory(t *testing.T) {
	lines := describeTriggers(pipelines.Triggers{
		PullRequest: &pipelines.PullRequestTrigger{Branches: []string{"main"}},
	})
	if len(lines) != 1 || lines[0].Advisory == "" {
		t.Fatalf("a declared branches filter is not marked advisory: %+v", lines)
	}
	if !strings.Contains(lines[0].Advisory, "branches") {
		t.Errorf("advisory does not name the field: %q", lines[0].Advisory)
	}

	plain := describeTriggers(pipelines.Triggers{PullRequest: &pipelines.PullRequestTrigger{}})
	if plain[0].Advisory != "" {
		t.Errorf("advisory fired with no filter declared: %q", plain[0].Advisory)
	}
}

func TestEveryTriggerKindRenders(t *testing.T) {
	all := pipelines.Triggers{
		Push:           &pipelines.PushTrigger{},
		PullRequest:    &pipelines.PullRequestTrigger{},
		Schedule:       pipelines.ScheduleTriggers{{Cron: "0 9 * * *", Where: "local"}},
		Webhook:        &pipelines.WebhookTrigger{Path: "/review"},
		PreHook:        &pipelines.PreHookTrigger{},
		PostHook:       &pipelines.PostHookTrigger{},
		PostCommitHook: &pipelines.PostCommitHookTrigger{},
	}
	lines := describeTriggers(all)
	if len(lines) != 7 {
		t.Fatalf("got %d lines for 7 declared triggers: %+v", len(lines), lines)
	}
	if len(describeTriggers(pipelines.Triggers{})) != 0 {
		t.Error("an empty Triggers rendered a line")
	}
}

func TestScheduleDetail(t *testing.T) {
	for _, tc := range []struct {
		name    string
		trigger pipelines.ScheduleTrigger
		want    string
	}{
		{"defaults", pipelines.ScheduleTrigger{Cron: "0 9 * * *", Where: "local"},
			"default: 0 9 * * * (UTC) where local"},
		{"named entry", pipelines.ScheduleTrigger{Name: "cluster", Cron: "0 9 * * *", Where: "controller"},
			"cluster: 0 9 * * * (UTC) where controller"},
		{"zone", pipelines.ScheduleTrigger{Cron: "0 9 * * *", TZ: "America/Denver", Where: "local"},
			"default: 0 9 * * * (America/Denver) where local"},
		{"default overlap stays quiet", pipelines.ScheduleTrigger{Cron: "0 9 * * *", Overlap: "skip", Where: "local"},
			"default: 0 9 * * * (UTC) where local"},
		{"policies", pipelines.ScheduleTrigger{Cron: "0 9 * * *", Overlap: "queue", CatchUp: "6h", Where: "local"},
			"default: 0 9 * * * (UTC) where local, overlap queue, catch-up 6h0m0s"},
		{"args", pipelines.ScheduleTrigger{Cron: "0 9 * * *", Where: "local", Args: map[string]string{"region": "us-east", "dry-run": "true"}},
			"default: 0 9 * * * (UTC) where local, arg dry-run=true, arg region=us-east"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := scheduleDetail(&tc.trigger); got != tc.want {
				t.Errorf("scheduleDetail = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestEveryScheduleEntryGetsALine(t *testing.T) {
	lines := describeTriggers(pipelines.Triggers{Schedule: pipelines.ScheduleTriggers{
		{Name: "nightly", Cron: "0 3 * * *", Where: "local"},
		{Name: "cluster", Cron: "0 3 * * *", Where: "controller"},
	}})
	if len(lines) != 2 {
		t.Fatalf("got %d lines for 2 schedule entries: %+v", len(lines), lines)
	}
	if !strings.Contains(lines[0].Detail, "nightly") || !strings.Contains(lines[1].Detail, "cluster") {
		t.Errorf("lines do not name their entries: %+v", lines)
	}
}
