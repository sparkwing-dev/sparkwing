package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/repos"
)

func TestRepositoryUpdatesApplyToSelectedCheckoutAndPreviewExplicitly(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	t.Chdir(root)
	fleet := []repos.Repo{{Name: "selected", Primary: root}, {Name: "other", Primary: other}}
	for _, tc := range []struct {
		name  string
		flags []string
		apply bool
		want  []repos.Repo
	}{
		{"selected", nil, true, fleet[:1]},
		{"selected preview", []string{"--dry-run"}, false, fleet[:1]},
		{"fleet preview", []string{"--all-repos", "--dry-run"}, false, fleet},
		{"confirmed fleet", []string{"--all-repos", "--yes"}, true, fleet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			var commandErr error
			captureStdout(t, func() {
				commandErr = runReposUpdateWithArgs(append([]string{"--version", "v1.2.3", "-o=json"}, tc.flags...),
					func(string) ([]repos.Repo, error) { return fleet, nil },
					func(got []repos.Repo, config repos.UpdateConfig) []repos.Verdict {
						called = true
						if !reflect.DeepEqual(got, tc.want) || config.Apply != tc.apply || config.Target != "v1.2.3" || config.Context == nil {
							t.Errorf("update: %+v, config=%+v", got, config)
						}
						return nil
					})
			})
			if commandErr != nil || !called {
				t.Fatalf("update was not dispatched: %v", commandErr)
			}
		})
	}
}

func TestRepositoryUpdatesRefuseUnconfirmedFleetBeforeReadingIt(t *testing.T) {
	err := runReposUpdateWithArgs([]string{"--all-repos", "--version", "v1.2.3"},
		func(string) ([]repos.Repo, error) { t.Fatal("read fleet before confirmation"); return nil, nil },
		func([]repos.Repo, repos.UpdateConfig) []repos.Verdict {
			t.Fatal("changed unconfirmed fleet")
			return nil
		})
	if err == nil || exitCodeFor(err) != 2 {
		t.Fatalf("unconfirmed fleet: %v", err)
	}
	t.Setenv("SPARKWING_HOME", t.TempDir())
	if err := runSparkwing([]string{"repos", "update", "--all-repos", "--version", "v1.2.3"}); err == nil || exitCodeFor(err) != 2 {
		t.Fatalf("public unconfirmed fleet: %v", err)
	}
}

func TestRepositoryUpdateSelectionRejectsSiblingPrefixesProperty(t *testing.T) {
	parent := t.TempDir()
	for i := range 32 {
		primary := filepath.Join(parent, fmt.Sprintf("repo-%d", i))
		checkout := primary + "-other"
		if err := os.Mkdir(checkout, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Chdir(checkout)
		err := runReposUpdateWithArgs([]string{"--version", "v1.2.3"},
			func(string) ([]repos.Repo, error) { return []repos.Repo{{Name: "sibling", Primary: primary}}, nil },
			func([]repos.Repo, repos.UpdateConfig) []repos.Verdict {
				t.Fatal("updated sibling checkout")
				return nil
			})
		if err == nil {
			t.Fatalf("%q selected sibling %q", checkout, primary)
		}
	}
}

func TestRepositoryUpdatePreviewDoesNotWriteStartupState(t *testing.T) {
	previous := Version
	Version = "v1.2.3"
	t.Cleanup(func() { Version = previous })
	home := t.TempDir()
	t.Setenv("SPARKWING_HOME", home)
	err := runSparkwing([]string{"repos", "update", "--in-place", "--dry-run", "--version", "invalid"})
	if err == nil || exitCodeFor(err) != 2 {
		t.Fatalf("invalid preview target: %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil || len(entries) != 0 {
		t.Fatalf("preview startup changed state: %v, %v", entries, err)
	}
}
