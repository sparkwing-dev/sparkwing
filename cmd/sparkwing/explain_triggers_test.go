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
		Schedule:       "0 9 * * *",
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
