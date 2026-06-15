package sparkwing

import (
	"os"
	"testing"
)

// detectRuntime no longer reads SPARKWING_RUN_ID / SPARKWING_NODE_ID
// / SPARKWING_DEBUG / SPARKWING_HOST / KUBERNETES_SERVICE_HOST. The
// only env var that survives at this layer is the implicit cwd; the
// resulting struct must be byte-identical regardless of the legacy
// env signals.
func TestDetectRuntime_IgnoresLegacyEnvSignals(t *testing.T) {
	for _, k := range []string{
		"SPARKWING_RUN_ID", "SPARKWING_NODE_ID", "SPARKWING_DEBUG",
		"SPARKWING_HOST", "KUBERNETES_SERVICE_HOST",
	} {
		t.Setenv(k, "would-have-mattered-before-step-10")
	}
	rc := detectRuntime()
	if rc.Git == nil {
		t.Fatal("Git pre-populate failed: nil")
	}
	for _, env := range []string{
		"would-have-mattered-before-step-10",
		os.Getenv("SPARKWING_RUN_ID"),
	} {
		if env != "" && rc.WorkDir == env {
			t.Errorf("WorkDir bled from env: %q", rc.WorkDir)
		}
	}
}

// detectRuntime ignores SPARKWING_WORK_DIR even when set, since the
// stale-env-var hijack scenario was the bug we were trying to
// eliminate. Walk-up from cwd is the sole source of truth.
func TestDetectRuntime_IgnoresLegacyWorkDirEnv(t *testing.T) {
	t.Setenv("SPARKWING_WORK_DIR", "/some/stale/path/that/does/not/exist")
	rc := detectRuntime()
	if rc.WorkDir == "/some/stale/path/that/does/not/exist" {
		t.Errorf("detectRuntime honored stale SPARKWING_WORK_DIR; "+
			"env-var must not influence WorkDir. got=%q", rc.WorkDir)
	}
}

// walkUpToProject finds `.sparkwing/` ascending from start and
// returns its parent. Mirrors how git locates a repo root.
func TestWalkUpToProject_FindsMarker(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(root+"/sub/deep", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root+"/.sparkwing", 0o755); err != nil {
		t.Fatal(err)
	}
	got := walkUpToProject(root + "/sub/deep")
	if got != root {
		t.Fatalf("walkUpToProject = %q, want %q", got, root)
	}
}

// walkUpToProject returns "" when there's no `.sparkwing/` above
// start. Helpers see this and refuse to run rather than silently
// fall back to cwd.
func TestWalkUpToProject_ReturnsEmptyWhenNoProject(t *testing.T) {
	root := t.TempDir()
	got := walkUpToProject(root)
	if got != "" {
		t.Fatalf("walkUpToProject = %q in a project-less dir, want empty", got)
	}
}

// SetGit attaches a populated *Git, visible via CurrentRuntime().Git.
func TestSetGit_AttachesPopulatedGit(t *testing.T) {
	prev := runtime.Git
	t.Cleanup(func() { runtimeMu.Lock(); runtime.Git = prev; runtimeMu.Unlock() })

	g := NewGit("/work/repo", "abc123def456abc123def456abc123def456abcd",
		"main", "main", "owner/name", "git@github.com:owner/name.git")
	SetGit(g)
	got := CurrentRuntime().Git
	if got == nil {
		t.Fatal("CurrentRuntime().Git is nil after SetGit")
	}
	if got.SHA != g.SHA || got.Branch != g.Branch || got.Repo != g.Repo {
		t.Errorf("SetGit roundtrip mismatch: %+v vs %+v", got, g)
	}
	if got.ShortSHA() != "abc123def456" {
		t.Errorf("ShortSHA = %q", got.ShortSHA())
	}
}
