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
	got, err := TriggerSourceURL(&store.Trigger{RepoURL: "https://git.example.com/acme/widgets.git"}, true)
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

// The run page shows GITHUB_REPOSITORY while a direct runner fetched
// git.repo_url, so a trigger whose fields disagree could show one repository
// and run another.
func TestTriggerSourceURLRefusesATriggerThatNamesTwoRepositories(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	for _, trigger := range []*store.Trigger{
		{RepoURL: "https://github.com/evil/payload.git", TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "acme/app"}},
		{RepoURL: "https://github.com/acme/app.git", GithubOwner: "evil", GithubRepo: "payload"},
		{TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "acme/app"}, GithubOwner: "evil", GithubRepo: "payload"},
	} {
		for _, direct := range []bool{true, false} {
			if got, err := TriggerSourceURL(trigger, direct); err == nil {
				t.Errorf("TriggerSourceURL(%+v, direct=%v) = %q, want a refusal", trigger, direct, got)
			}
		}
	}
}

// Both paths fetch the same repository, each in the form its transport needs.
func TestTriggerSourceURLNamesOneRepositoryOnBothPaths(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	trigger := &store.Trigger{
		RepoURL:    "https://github.com/acme/app.git",
		TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "acme/app"},
	}
	direct, err := TriggerSourceURL(trigger, true)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := TriggerSourceURL(trigger, false)
	if err != nil {
		t.Fatal(err)
	}
	if direct != "https://github.com/acme/app.git" || cached != "git@github.com:acme/app.git" {
		t.Fatalf("direct = %q, cached = %q", direct, cached)
	}
}
