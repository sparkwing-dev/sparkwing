package jobs

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func readOnlyDeployCache(t *testing.T, root string) {
	t.Helper()
	module := filepath.Join(root, "cli-home", "go", "pkg", "mod", "example.com", "module@v1.0.0")
	nested := filepath.Join(module, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nested, "README.md"), []byte("cached module"), 0o400); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, dir := range []string{module, nested} {
			if err := os.Chmod(dir, 0o700); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
		}
	})
	for _, dir := range []string{nested, module} {
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSweepStaleDeployDirsRemovesReadOnlyCache(t *testing.T) {
	root := t.TempDir()
	stale := filepath.Join(root, deployDirPrefix+"run-old-staging-gate-1")
	readOnlyDeployCache(t, stale)
	now := time.Now()
	old := now.Add(-5 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if err := sweepStaleDeployDirs(root, now, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale directory remains: %v", err)
	}
}

func TestDeployCleanupRemovesReadOnlyCache(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	cfg := defaultDeployConfig()
	cfg.ToolDir = ""
	j := deployJob{Cfg: cfg, RunID: "run-cleanup", Name: "staging-gate"}
	d, done, err := j.deployer()
	if err != nil {
		t.Fatal(err)
	}
	readOnlyDeployCache(t, d.tmp)
	done()
	if _, err := os.Stat(d.tmp); !os.IsNotExist(err) {
		t.Fatalf("job directory remains after cleanup: %v", err)
	}
}

func TestDeployCLIUsesWritableModuleCache(t *testing.T) {
	f := newFakeDeploy(t)
	d := f.deployer(false)
	run := d.tools.run
	built := false
	triggered := false
	d.tools.run = func(ctx context.Context, c deployCommand) (string, error) {
		if filepath.Base(c.Name) == "go" {
			built = true
		}
		if c.Name == d.cliBin() {
			triggered = true
		}
		if filepath.Base(c.Name) == "go" || c.Name == d.cliBin() {
			if c.Env["GOFLAGS"] != "-modcacherw" {
				t.Errorf("%s GOFLAGS = %q, want -modcacherw", c.Name, c.Env["GOFLAGS"])
			}
		}
		return run(ctx, c)
	}
	if err := d.buildCLI(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !built {
		t.Fatal("candidate CLI was not built")
	}
	var runID string
	if err := d.triggerRun(t.Context(), "staging", "cloud", "example", d.repoDir, "", &runID); err != nil {
		t.Fatal(err)
	}
	if !triggered {
		t.Fatal("candidate CLI did not trigger the pipeline")
	}
}
