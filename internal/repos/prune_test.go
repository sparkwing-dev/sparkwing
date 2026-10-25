package repos

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func TestPruneDropsOnlyStaleCheckouts(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "config.yaml")
	live := filepath.Join(root, "live")
	stale := filepath.Join(root, "stale")
	if err := os.MkdirAll(filepath.Join(live, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_CONFIG", registry)
	want := &userconfig.ReposConfig{
		Repos:         []userconfig.RepoPath{{Path: live}, {Path: stale}},
		FallbackPaths: []string{"~/code"},
	}
	if err := Save(registry, want); err != nil {
		t.Fatal(err)
	}

	dropped, err := Prune()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dropped, []string{stale}) {
		t.Fatalf("dropped = %v, want [%s]", dropped, stale)
	}
	got, err := load(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Repos, []userconfig.RepoPath{{Path: live}}) {
		t.Fatalf("repos = %#v, want only %s", got.Repos, live)
	}
	if !reflect.DeepEqual(got.FallbackPaths, want.FallbackPaths) {
		t.Fatalf("fallback paths = %v, want %v", got.FallbackPaths, want.FallbackPaths)
	}
}

func TestRegistryMutationsPreserveSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	t.Setenv(userconfig.PathEnv, path)
	enabled, disabled := true, false
	want := userconfig.ReposConfig{AutoRegister: &disabled, IncludeWorktrees: &enabled, FallbackPaths: []string{"~/code"}}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"add", "remove", "prune"} {
		t.Run(operation, func(t *testing.T) {
			if err := userconfig.Write(path, userconfig.Repos, "test registry", &want); err != nil {
				t.Fatal(err)
			}
			if err := Add(root); err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "remove":
				if _, err := Remove(root); err != nil {
					t.Fatal(err)
				}
			case "prune":
				if _, err := Prune(); err != nil {
					t.Fatal(err)
				}
			}
			var got userconfig.ReposConfig
			if _, err := userconfig.Read(path, userconfig.Repos, &got); err != nil {
				t.Fatal(err)
			}
			got.Repos = nil
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("settings after %s = %+v, want %+v", operation, got, want)
			}
		})
	}
}
