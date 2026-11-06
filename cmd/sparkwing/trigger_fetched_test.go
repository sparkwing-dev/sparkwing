package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/source/sourcetest"
)

func TestTriggerPreflightFromRunnerSourceCheckout(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	const remote = "https://8.8.8.8/team/repo"
	sourcetest.Remote(t, map[string]string{remote: repo})
	entry, err := source.PrepareCheckout(t.Context(), source.Spec{
		RepoKey: "git:8.8.8.8/team/repo", RemoteURL: remote, SHA: sha, DefaultBranch: "main",
	}, source.Options{
		Workspace: t.TempDir(), Access: snapshotRemoteAccess{}, BuildEnvironment: sourcetest.BuildEnvironment,
		Base: procenv.Base{Ambient: sourcetest.Environment(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := entry.Close(); err != nil {
			t.Error(err)
		}
	})
	checkout := entry.CheckoutDirectory()
	fetchHead := filepath.Join(checkout, ".git", "FETCH_HEAD")
	fetched, err := os.ReadFile(fetchHead)
	if err != nil {
		t.Fatal(err)
	}
	// hack: the fixture transport substitutes a local path for the remote URL.
	if err := os.WriteFile(fetchHead, []byte(strings.ReplaceAll(string(fetched), repo, remote)), 0o600); err != nil {
		t.Fatal(err)
	}
	input := triggerPreparationInput(checkout)
	input.ref = sha
	selected, err := prepareTriggerSource(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if selected.git.SHA != sha || selected.source.Ref != sha || selected.source.Location.RemoteURL != remote {
		t.Fatalf("selected revisions = %+v %+v", selected.git, selected.source)
	}
	if refs := runSnapshotGit(t, checkout, "for-each-ref", "--format=%(refname)", "refs/remotes/origin/"); refs != "" {
		t.Fatalf("raw SHA fetch unexpectedly created tracking refs: %s", refs)
	}
	if err := selected.checkReachable(t.Context()); err != nil {
		t.Fatalf("runner-fetched pushed commit refused: %v", err)
	}
	for _, record := range []string{
		sha + "\t\t'" + sha + "' of https://8.8.8.8/other/repo\n",
		sha + "\t\tbranch 'main' of " + remote + "\n",
		strings.Repeat("a", 40) + "\t\t'" + sha + "' of " + remote + "\n",
	} {
		if err := os.WriteFile(fetchHead, []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := selected.checkReachable(t.Context()); err == nil {
			t.Fatalf("unrelated fetch accepted: %q", record)
		}
	}
	if err := os.Remove(fetchHead); err != nil {
		t.Fatal(err)
	}
	if err := selected.checkReachable(t.Context()); err == nil {
		t.Fatal("missing fetch evidence accepted")
	}
}

func TestTriggerPreflightFromReferenceClone(t *testing.T) {
	repo, sha := triggerPreparationRepo(t)
	checkout := filepath.Join(t.TempDir(), "reference")
	runSnapshotGit(t, filepath.Dir(checkout), "clone", "--quiet", "--depth=1", "file://"+filepath.ToSlash(repo), checkout)
	runSnapshotGit(t, checkout, "remote", "set-url", "origin", "https://github.com/acme/reference.git")
	selected, err := prepareTriggerSource(t.Context(), triggerPreparationInput(checkout))
	if err != nil {
		t.Fatal(err)
	}
	if selected.git.SHA != sha || selected.source.Ref != sha {
		t.Fatalf("reference clone selected different revisions: %+v %+v", selected.git, selected.source)
	}
	if err := selected.checkReachable(t.Context()); err != nil {
		t.Fatalf("reference clone refused: %v", err)
	}
}

func TestTriggerPreflightRequiresBothRevisionsWithFetchEvidence(t *testing.T) {
	repo, pushed, local := pushedAndLocalCommits(t)
	const cloneURL = "https://8.8.8.8/team/repo"
	runSnapshotGit(t, repo, "remote", "set-url", "origin", cloneURL)
	selected := preparedTriggerSource{
		git: runspec.Git{SHA: local}, source: runspec.Source{Ref: pushed},
		checkout: triggerCheckout{directory: repo, environment: os.Environ()},
	}
	runSnapshotGit(t, repo, "update-ref", "-d", "refs/remotes/origin/main")
	for _, commits := range [][]string{{pushed}, {local}, {local, pushed}} {
		var records string
		for _, sha := range commits {
			records += sha + "\t\t'" + sha + "' of " + cloneURL + "\n"
		}
		if err := os.WriteFile(filepath.Join(repo, ".git", "FETCH_HEAD"), []byte(records), 0o600); err != nil {
			t.Fatal(err)
		}
		err := selected.checkReachable(t.Context())
		if (err == nil) != (len(commits) == 2) {
			t.Fatalf("fetches %v: %v", commits, err)
		}
	}
	for _, origin := range []string{cloneURL + ".git", cloneURL + ".git/", cloneURL + "/"} {
		runSnapshotGit(t, repo, "remote", "set-url", "origin", origin)
		if err := selected.checkReachable(t.Context()); err != nil {
			t.Fatalf("Git's shortened fetch URL for origin %s refused: %v", origin, err)
		}
	}
	for _, remote := range []struct{ origin, description string }{
		{"git@8.8.8.8:team/repo.git", "8.8.8.8:team/repo"},
		{"ssh://git@8.8.8.8:2222/team/repo.git", "ssh://8.8.8.8:2222/team/repo"},
	} {
		runSnapshotGit(t, repo, "remote", "set-url", "origin", remote.origin)
		record := pushed + "\t\t'" + pushed + "' of " + remote.description + "\n"
		if err := os.WriteFile(filepath.Join(repo, ".git", "FETCH_HEAD"), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
		selected.git.SHA = pushed
		if err := selected.checkReachable(t.Context()); err != nil {
			t.Fatalf("Git's fetch description for origin %s refused: %v", remote.origin, err)
		}
	}
}
