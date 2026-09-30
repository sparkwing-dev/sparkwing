package orchestrator

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/sourceurl"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func mustAllow(t *testing.T, patterns ...string) sourceurl.RepoAllowlist {
	t.Helper()
	allow, err := sourceurl.ParseRepoAllowlist(patterns)
	if err != nil {
		t.Fatal(err)
	}
	return allow
}

func TestAdmitTriggerSourceChecksTheFetchedRemoteAndTheGitHubName(t *testing.T) {
	allow := mustAllow(t, "github.com/acme/*")
	admitted := []*store.Trigger{
		{RepoURL: "git@github.com:Acme/App.git"},
		{TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "acme/app"}},
		{GithubOwner: "acme", GithubRepo: "app", RepoURL: "https://github.com/acme/app"},
	}
	for _, tr := range admitted {
		fetch, err := TriggerSourceURL(tr, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := AdmitTriggerSource(allow, tr, fetch); err != nil {
			t.Errorf("AdmitTriggerSource(%+v) = %v, want admitted", tr, err)
		}
	}
	refused := []*store.Trigger{
		{ID: "r1", RepoURL: "https://github.com/evil/payload.git"},
		{ID: "r2", TriggerEnv: map[string]string{"GITHUB_REPOSITORY": "evil/payload"}},
		{ID: "r3", RepoURL: "https://gitlab.com/acme/app.git"},
	}
	for _, tr := range refused {
		fetch, err := TriggerSourceURL(tr, true)
		if err != nil {
			t.Fatal(err)
		}
		err = AdmitTriggerSource(allow, tr, fetch)
		if !errors.Is(err, ErrRepoNotAllowed) || !strings.Contains(err.Error(), "github.com/acme/*") {
			t.Errorf("AdmitTriggerSource(%+v) = %v, want a refusal naming the allowlist", tr, err)
		}
	}
	// safety: the fetched remote is checked on its own, since it is what runs.
	err := AdmitTriggerSource(allow, &store.Trigger{ID: "r4"}, "https://github.com/evil/payload.git")
	if !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("a fetch URL outside the list = %v, want a refusal", err)
	}
	if err := AdmitTriggerSource(sourceurl.RepoAllowlist{}, admitted[0], "git@github.com:acme/app.git"); !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("the empty allowlist = %v, want a refusal", err)
	}
}

// A pool runner that fetches with its own credentials claims nodes of any run
// in its team, so the node path must refuse a repository the owner did not
// allow before anything is fetched.
func TestRunNodeRemoteRefusesARepositoryOutsideTheAllowlistBeforeFetching(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	t.Setenv("SPARKWING_CACHE_URL", "")
	trigger := &store.Trigger{
		ID: "run-1", RepoURL: "https://git.invalid/evil/payload.git",
		GitSHA: "0123456789abcdef0123456789abcdef01234567",
	}
	allow := mustAllow(t, "github.com/acme/*")
	_, err := runNodeRemote(context.Background(), trigger, &store.Run{ID: "run-1", Pipeline: "demo"},
		"http://controller.invalid", "", "", "", "run-1", "node-1", "", &allow,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if !errors.Is(err, ErrRepoNotAllowed) {
		t.Fatalf("runNodeRemote = %v, want ErrRepoNotAllowed", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, "source-direct")); !os.IsNotExist(statErr) {
		t.Fatalf("a refused node still reached the fetch: %v", statErr)
	}
}

// A compromised controller chooses a working-tree snapshot's code, and a
// trigger naming no repository gives the list nothing to check, so an owner's
// list refuses both unless the owner accepts snapshots of listed repositories.
func TestRunNodeRemoteRefusesUncheckableSourcesBeforeFetching(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	snapshot := func(repo string) *store.Trigger {
		return &store.Trigger{
			ID: "run-1", RepoURL: repo, GitSHA: sha, TriggerSource: "pipeline-working-tree@laptop",
			TriggerEnv: map[string]string{"SPARKWING_SOURCE_BUNDLE_OBJECT": "sources/" + strings.Repeat("a", 64) + "/" + strings.Repeat("b", 32)},
		}
	}
	allow := mustAllow(t, "github.com/acme/*")
	for _, tc := range []struct {
		name    string
		trigger *store.Trigger
		allow   sourceurl.RepoAllowlist
	}{
		{"snapshot naming no repository", snapshot(""), allow},
		{"snapshot naming no repository with snapshots accepted", snapshot(""), allow.AcceptingWorkingTree()},
		{"snapshot of a listed repository", snapshot("https://github.com/acme/app.git"), allow},
		{"snapshot of an unlisted repository with snapshots accepted", snapshot("https://github.com/evil/app.git"), allow.AcceptingWorkingTree()},
		{"trigger naming no repository", &store.Trigger{ID: "run-1", GitSHA: sha}, allow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("SPARKWING_HOME", home)
			t.Setenv("SPARKWING_CACHE_URL", "")
			_, err := runNodeRemote(context.Background(), tc.trigger, &store.Run{ID: "run-1", Pipeline: "demo"},
				"http://controller.invalid", "", "", "grant", "run-1", "node-1", "", &tc.allow,
				slog.New(slog.NewTextHandler(io.Discard, nil)))
			if !errors.Is(err, ErrRepoNotAllowed) {
				t.Fatalf("runNodeRemote = %v, want ErrRepoNotAllowed", err)
			}
			if _, statErr := os.Stat(filepath.Join(home, "node-runner")); !os.IsNotExist(statErr) {
				t.Fatalf("a refused node still prepared its fetch: %v", statErr)
			}
		})
	}

	fetch, err := TriggerSourceURL(snapshot("https://github.com/acme/app.git"), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := AdmitTriggerSource(allow.AcceptingWorkingTree(), snapshot("https://github.com/acme/app.git"), fetch); err != nil {
		t.Fatalf("a snapshot of a listed repository with snapshots accepted = %v, want admitted", err)
	}
}
