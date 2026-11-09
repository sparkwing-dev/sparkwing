package cachecontrol

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/bincache"
)

func TestSharedHomesReuseOnlyMatchingInputs(t *testing.T) {
	source := t.TempDir()
	t.Setenv("SPARKWING_HOME", source)
	dir := t.TempDir()
	for name, body := range map[string]string{"go.mod": "module fixture\ngo 1.26\n", "main.go": "package main\nfunc main(){}\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"PATH=" + t.TempDir(), "GOWORK=off"}
	key, _, err := bincache.ExplainCacheKey(dir, env)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := bincache.PipelineEntry(key)
	if err != nil {
		t.Fatal(err)
	}
	seed, _, err := entry.AcquireOrMaterialize(t.Context(), func(path string) error {
		return os.WriteFile(path, []byte("verified pipeline"), 0o700)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Release(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		home := t.TempDir()
		t.Setenv("SPARKWING_HOME", source)
		for range 2 {
			if err := ShareWithHome(home); err != nil {
				t.Fatal(err)
			}
		}
		t.Setenv("SPARKWING_HOME", home)
		options := bincache.CompileOptions{Environment: env, Diagnostics: io.Discard}
		lease, built, err := bincache.MaterializePipeline(t.Context(), dir, options)
		if err != nil || built {
			t.Fatalf("fresh home rebuilt matching inputs: %t %v", built, err)
		}
		binary, installErr := lease.Install(filepath.Join(t.TempDir(), "pipeline"))
		if err := lease.Release(); err != nil {
			t.Fatal(err)
		}
		if installErr != nil {
			t.Fatal(installErr)
		}
		if body, err := os.ReadFile(binary.Path()); err != nil || string(body) != "verified pipeline" {
			t.Fatalf("installed bytes = %q, %v", body, err)
		}
		for _, setting := range []string{"GOOS=other", "GOARCH=arm64", "GOTOOLCHAIN=go1.26.9", "GOFLAGS=-tags=other"} {
			changed := options
			changed.Environment = append(append([]string(nil), env...), setting)
			if _, _, err := bincache.MaterializePipeline(t.Context(), dir, changed); err == nil {
				t.Fatalf("%s reused the wrong binary without a compiler", setting)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main(){print(2)}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := bincache.MaterializePipeline(t.Context(), dir, bincache.CompileOptions{Environment: env, Diagnostics: io.Discard}); err == nil {
		t.Fatal("changed source reused a stale pipeline without a compiler")
	}
}

func TestSharePreservesPrivateStateAndSurvivesJobCleanup(t *testing.T) {
	source := t.TempDir()
	t.Setenv("SPARKWING_HOME", source)
	home := t.TempDir()
	for _, name := range []string{"config.yaml", "state.db", "secrets.key"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := ShareWithHome(home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cache", "pipelines", "sentinel"), []byte("cache"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.yaml", "state.db", "secrets.key"} {
		if body, err := os.ReadFile(filepath.Join(home, name)); err != nil || string(body) != "private" {
			t.Fatalf("private file changed: %s %q %v", name, body, err)
		}
		if _, err := os.Stat(filepath.Join(source, name)); !os.IsNotExist(err) {
			t.Fatalf("private file leaked into shared home: %s %v", name, err)
		}
	}
	if err := os.RemoveAll(home); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(source, "cache", "pipelines", "sentinel")); err != nil || string(body) != "cache" {
		t.Fatalf("job cleanup removed shared cache: %q %v", body, err)
	}
	if err := os.MkdirAll(filepath.Join(home, "cache", "pipelines"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ShareWithHome(home); err == nil {
		t.Fatal("existing private cache was replaced")
	}
}
