package main

import (
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/pipelines"
)

// explain is offline. It reads sparkwing.yaml and nothing else, so it
// knows a trigger was declared and cannot know whether GitHub delivers
// it. An earlier version said "not yet live", which told anyone with a
// correctly configured webhook that their trigger did not work.
func TestTriggerNotesClaimOnlyWhatExplainKnows(t *testing.T) {
	lines := describeTriggers(pipelines.Triggers{
		PullRequest: &pipelines.PullRequestTrigger{},
		Push:        &pipelines.PushTrigger{},
	})
	if len(lines) != 2 {
		t.Fatalf("got %d trigger lines, want 2", len(lines))
	}
	for _, l := range lines {
		low := strings.ToLower(l.Blocker)
		for _, claim := range []string{"not yet live", "no webhook", "is not configured", "missing"} {
			if strings.Contains(low, claim) {
				t.Errorf("%s asserts %q about GitHub, which explain never checked: %q", l.Event, claim, l.Blocker)
			}
		}
		if l.Blocker == "" {
			t.Errorf("%s says nothing about what delivery depends on", l.Event)
		}
	}
}

// A declared filter that nothing reads has to be marked, or `branches:
// [main]` reads as scoping a trigger it does not scope.
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
	// And stays silent when no filter is declared.
	plain := describeTriggers(pipelines.Triggers{PullRequest: &pipelines.PullRequestTrigger{}})
	if plain[0].Advisory != "" {
		t.Errorf("advisory fired with no filter declared: %q", plain[0].Advisory)
	}
}

// Every trigger kind the config accepts has to render. A kind that
// decodes but does not appear here is invisible in exactly the way this
// section exists to prevent.
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
