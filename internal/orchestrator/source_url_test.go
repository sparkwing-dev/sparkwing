package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestShouldRunRemoteAcceptsStoredRepoURL(t *testing.T) {
	trigger := &store.Trigger{RepoURL: "https://git.netwits.com/InevitableAI/regent.git"}
	if !shouldRunRemote(trigger) {
		t.Fatal("shouldRunRemote = false, want true for stored repo URL")
	}
}

func TestRemoteTriggerSourceURLPrefersGitHubMetadata(t *testing.T) {
	trigger := &store.Trigger{
		RepoURL: "https://git.netwits.com/InevitableAI/regent.git",
		TriggerEnv: map[string]string{
			"GITHUB_REPOSITORY": "sparkwing-dev/sparkwing",
		},
	}

	got, err := remoteTriggerSourceURL(trigger)
	if err != nil {
		t.Fatalf("remoteTriggerSourceURL: %v", err)
	}
	if got != "git@github.com:sparkwing-dev/sparkwing.git" {
		t.Fatalf("remoteTriggerSourceURL = %q, want canonical GitHub SSH URL", got)
	}
}

func TestRemoteTriggerSourceURLUsesStoredRepoURLWithoutGitHubMetadata(t *testing.T) {
	trigger := &store.Trigger{RepoURL: "https://git.netwits.com/InevitableAI/regent.git"}

	got, err := remoteTriggerSourceURL(trigger)
	if err != nil {
		t.Fatalf("remoteTriggerSourceURL: %v", err)
	}
	if got != "https://git.netwits.com/InevitableAI/regent.git" {
		t.Fatalf("remoteTriggerSourceURL = %q, want stored repo URL", got)
	}
}
