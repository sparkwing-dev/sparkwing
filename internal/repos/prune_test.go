package repos

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPruneDropsOnlyStaleCheckouts(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "repos.yaml")
	live := filepath.Join(root, "live")
	stale := filepath.Join(root, "stale")
	if err := os.MkdirAll(filepath.Join(live, ".sparkwing"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stale, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPARKWING_REPOS", registry)
	want := &Config{
		Repos:         []*Entry{{Path: live}, {Path: stale}},
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
	got, err := Load(registry)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Repos, []*Entry{{Path: live}}) {
		t.Fatalf("repos = %#v, want only %s", got.Repos, live)
	}
	if !reflect.DeepEqual(got.FallbackPaths, want.FallbackPaths) {
		t.Fatalf("fallback paths = %v, want %v", got.FallbackPaths, want.FallbackPaths)
	}
}
