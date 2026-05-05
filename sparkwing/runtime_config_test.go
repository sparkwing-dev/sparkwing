package sparkwing

import (
	"os"
	"testing"
)

// detectIsLocal is the truth function the package-level var calls
// at init. Re-test it with various env states so any future change
// to the signal list is caught here.
func TestDetectIsLocal(t *testing.T) {
	cases := []struct {
		name           string
		sparkwingHost  string
		k8sServiceHost string
		want           bool
	}{
		{"laptop bare", "", "", true},
		{"laptop with stale SPARKWING_HOST=laptop", "laptop", "", true},
		{"explicit cluster signal", "cluster", "", false},
		{"k8s pod (auto-injected)", "", "10.0.0.1", false},
		{"both signals (cluster wins)", "cluster", "10.0.0.1", false},
		{"explicit cluster overrides everything", "cluster", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.sparkwingHost == "" {
				os.Unsetenv("SPARKWING_HOST")
			} else {
				t.Setenv("SPARKWING_HOST", tc.sparkwingHost)
			}
			if tc.k8sServiceHost == "" {
				os.Unsetenv("KUBERNETES_SERVICE_HOST")
			} else {
				t.Setenv("KUBERNETES_SERVICE_HOST", tc.k8sServiceHost)
			}
			if got := detectIsLocal(); got != tc.want {
				t.Errorf("detectIsLocal() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseDebug(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"0", false},
		{"false", false},
		{"False", false},
		{"FALSE", false},
		{"1", true},
		{"true", true},
		{"yes", true},
	}
	for _, tc := range cases {
		t.Run("v="+tc.in, func(t *testing.T) {
			if got := parseDebug(tc.in); got != tc.want {
				t.Errorf("parseDebug(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// detectRuntime reads scalar fields (RunID, NodeID, Debug) from env;
// WorkDir is no longer env-driven -- it's discovered via walk-up to
// `.sparkwing/`. The env var SPARKWING_WORK_DIR is retired entirely
// to eliminate the stale-env footgun (Scenario D). Git state still
// arrives via SetGit from the orchestrator, not env.
func TestDetectRuntime_PopulatesScalarFieldsFromEnv(t *testing.T) {
	t.Setenv("SPARKWING_RUN_ID", "run-X")
	t.Setenv("SPARKWING_NODE_ID", "build-deploy")
	t.Setenv("SPARKWING_DEBUG", "1")
	t.Setenv("SPARKWING_HOST", "cluster")
	os.Unsetenv("KUBERNETES_SERVICE_HOST")

	rc := detectRuntime()

	if rc.IsLocal {
		t.Error("IsLocal should be false when SPARKWING_HOST=cluster")
	}
	if rc.RunID != "run-X" {
		t.Errorf("RunID = %q", rc.RunID)
	}
	if rc.NodeID != "build-deploy" {
		t.Errorf("NodeID = %q", rc.NodeID)
	}
	if !rc.Debug {
		t.Error("Debug should be true when SPARKWING_DEBUG=1")
	}
	if rc.Git == nil {
		t.Fatal("Git pre-populate failed: nil")
	}
}

// detectRuntime ignores SPARKWING_WORK_DIR even when set, since the
// stale-env-var hijack scenario (Scenario D) was the bug we were
// trying to eliminate. Walk-up from cwd is the sole source of truth.
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

// SetGit attaches a populated *Git, visible via Runtime().Git.
func TestSetGit_AttachesPopulatedGit(t *testing.T) {
	prev := runtime.Git
	t.Cleanup(func() { runtimeMu.Lock(); runtime.Git = prev; runtimeMu.Unlock() })

	g := NewGit("/work/repo", "abc123def456abc123def456abc123def456abcd",
		"main", "owner/name", "git@github.com:owner/name.git")
	SetGit(g)
	got := Runtime().Git
	if got == nil {
		t.Fatal("Runtime().Git is nil after SetGit")
	}
	if got.SHA != g.SHA || got.Branch != g.Branch || got.Repo != g.Repo {
		t.Errorf("SetGit roundtrip mismatch: %+v vs %+v", got, g)
	}
	if got.ShortSHA() != "abc123def456" {
		t.Errorf("ShortSHA = %q", got.ShortSHA())
	}
}

// RunConfig alias must keep working for sparks-core and friends.
func TestRunConfigAliasStillWorks(t *testing.T) {
	a := CurrentRuntime()
	b := CurrentRunConfig()
	if a.IsLocal != b.IsLocal || a.WorkDir != b.WorkDir {
		t.Errorf("alias mismatch: %#v vs %#v", a, b)
	}
	var _ RunConfig = RuntimeConfig{}
}
