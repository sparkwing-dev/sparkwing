package cluster

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestTriggerSourceURLPrefersGitHubMetadata(t *testing.T) {
	trigger := &store.Trigger{
		RepoURL: "https://git.example.com/acme/widgets.git",
		TriggerEnv: map[string]string{
			"GITHUB_REPOSITORY": "sparkwing-dev/sparkwing",
		},
	}

	got, err := triggerSourceURL(trigger)
	if err != nil {
		t.Fatalf("triggerSourceURL: %v", err)
	}
	if got != "git@github.com:sparkwing-dev/sparkwing.git" {
		t.Fatalf("triggerSourceURL = %q, want canonical GitHub SSH URL", got)
	}
}

func TestTriggerSourceURLUsesStoredRepoURLWithoutGitHubMetadata(t *testing.T) {
	trigger := &store.Trigger{RepoURL: "https://git.example.com/acme/widgets.git"}

	got, err := triggerSourceURL(trigger)
	if err != nil {
		t.Fatalf("triggerSourceURL: %v", err)
	}
	if got != "https://git.example.com/acme/widgets.git" {
		t.Fatalf("triggerSourceURL = %q, want stored repo URL", got)
	}
}

func TestTriggerSourceURLFallsBackToGitHubFields(t *testing.T) {
	trigger := &store.Trigger{
		GithubOwner: "sparkwing-dev",
		GithubRepo:  "sparkwing",
	}

	got, err := triggerSourceURL(trigger)
	if err != nil {
		t.Fatalf("triggerSourceURL: %v", err)
	}
	if got != "git@github.com:sparkwing-dev/sparkwing.git" {
		t.Fatalf("triggerSourceURL = %q, want GitHub SSH URL", got)
	}
}
