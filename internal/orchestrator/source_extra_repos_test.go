package orchestrator

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestPipelineExtraReposReadsTheFetchedConfig(t *testing.T) {
	checkout := t.TempDir()
	dir := filepath.Join(checkout, ".sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "pipelines:\n  - name: build\n    entrypoint: Build\n    source:\n      extra_repos: [acme/lib, acme/proto]\n" +
		"  - name: plain\n    entrypoint: Plain\n"
	if err := os.WriteFile(filepath.Join(dir, "sparkwing.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	for pipeline, want := range map[string][]string{
		"build": {"acme/lib", "acme/proto"}, "plain": {}, "absent": {},
	} {
		got, err := PipelineExtraRepos(pipeline)(checkout)
		if err != nil || got == nil || !slices.Equal(got, want) {
			t.Errorf("%s: extra repos = %#v, %v; want %#v", pipeline, got, err, want)
		}
	}
	if got, err := PipelineExtraRepos("build")(t.TempDir()); err != nil || got == nil || len(got) != 0 {
		t.Errorf("a checkout without a config = %#v, %v; want an empty declaration", got, err)
	}
}

// The config is the fetched tree's, so a sparkwing.yaml that is a symlink is
// refused rather than followed to a file elsewhere on the runner.
func TestPipelineExtraReposRefusesASymlinkedConfig(t *testing.T) {
	checkout := t.TempDir()
	dir := filepath.Join(checkout, ".sparkwing")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "elsewhere.yaml")
	if err := os.WriteFile(outside, []byte("pipelines: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "sparkwing.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := PipelineExtraRepos("build")(checkout); err == nil {
		t.Fatal("a symlinked config was followed")
	}
}
