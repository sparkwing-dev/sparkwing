package orchestrator

import (
	"testing"

	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func TestShouldRunRemoteAcceptsStoredRepoURL(t *testing.T) {
	trigger := &store.Trigger{RepoURL: "https://git.example.com/acme/widgets.git"}
	if !shouldRunRemote(trigger, false) {
		t.Fatal("shouldRunRemote = false, want true for stored repo URL")
	}
}

func TestRemoteTriggerSourceURLPrefersGitHubMetadata(t *testing.T) {
	trigger := &store.Trigger{
		RepoURL: "https://git.example.com/acme/widgets.git",
		TriggerEnv: map[string]string{
			"GITHUB_REPOSITORY": "sparkwing-dev/sparkwing",
		},
	}

	got, err := TriggerSourceURL(trigger, false)
	if err != nil {
		t.Fatalf("remoteTriggerSourceURL: %v", err)
	}
	if got != "git@github.com:sparkwing-dev/sparkwing.git" {
		t.Fatalf("remoteTriggerSourceURL = %q, want canonical GitHub SSH URL", got)
	}
}

func TestRemoteTriggerSourceURLUsesStoredRepoURLWithoutGitHubMetadata(t *testing.T) {
	trigger := &store.Trigger{RepoURL: "https://git.example.com/acme/widgets.git"}

	got, err := TriggerSourceURL(trigger, false)
	if err != nil {
		t.Fatalf("remoteTriggerSourceURL: %v", err)
	}
	if got != "https://git.example.com/acme/widgets.git" {
		t.Fatalf("remoteTriggerSourceURL = %q, want stored repo URL", got)
	}
}

func TestTriggerSourceURLFallsBackToGitHubFields(t *testing.T) {
	trigger := &store.Trigger{GithubOwner: "sparkwing-dev", GithubRepo: "sparkwing"}
	got, err := TriggerSourceURL(trigger, false)
	if err != nil {
		t.Fatalf("TriggerSourceURL: %v", err)
	}
	if got != "git@github.com:sparkwing-dev/sparkwing.git" {
		t.Fatalf("TriggerSourceURL = %q, want GitHub SSH URL", got)
	}
}

// A direct fetch goes where the trigger's author pushed, as they recorded it,
// and never to the ssh form the git cache registers.
func TestDirectTriggerSourceURLUsesTheRecordedRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	trigger := &store.Trigger{
		RepoURL:    "https://git.example.com/acme/widgets.git",
		TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "sparkwing-dev/sparkwing"},
	}
	got, err := TriggerSourceURL(trigger, true)
	if err != nil {
		t.Fatalf("TriggerSourceURL: %v", err)
	}
	if got != "https://git.example.com/acme/widgets.git" {
		t.Fatalf("TriggerSourceURL = %q, want the recorded remote", got)
	}

	got, err = TriggerSourceURL(&store.Trigger{GithubOwner: "sparkwing-dev", GithubRepo: "sparkwing"}, true)
	if err != nil {
		t.Fatalf("TriggerSourceURL: %v", err)
	}
	if got != "https://github.com/sparkwing-dev/sparkwing.git" {
		t.Fatalf("TriggerSourceURL = %q, want the https form of the GitHub name", got)
	}
}

// A cloud pod holds no ssh key, so a recorded GitHub ssh remote is fetched
// anonymously over https there.
func TestDirectTriggerSourceURLFetchesGitHubOverHTTPSWithoutAKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")
	got, err := TriggerSourceURL(&store.Trigger{RepoURL: "git@github.com:sparkwing-dev/sparkwing.git"}, true)
	if err != nil {
		t.Fatalf("TriggerSourceURL: %v", err)
	}
	if got != "https://github.com/sparkwing-dev/sparkwing.git" {
		t.Fatalf("TriggerSourceURL = %q, want https", got)
	}
}

func TestDirectTriggerSourceURLRefusesALocalRemote(t *testing.T) {
	for _, remote := range []string{"file:///srv/repo.git", "/srv/repo.git", "ext::sh -c id"} {
		if _, err := TriggerSourceURL(&store.Trigger{RepoURL: remote}, true); err == nil {
			t.Fatalf("TriggerSourceURL accepted %q", remote)
		}
	}
}
