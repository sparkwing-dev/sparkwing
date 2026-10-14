package orchestrator

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/paths"
)

func TestBuildRunInvocation_LogPathFromLocalLogBackend(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	dir := localRunLogDir(localLogs{paths: p}, "run-1")
	if want := p.RunDir("run-1"); dir != want {
		t.Fatalf("localRunLogDir = %q, want %q", dir, want)
	}
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", dir, nil)
	got, ok := inv["log_path"].(string)
	if !ok {
		t.Fatalf("log_path missing or not a string: %#v", inv["log_path"])
	}
	if got != p.RunDir("run-1") {
		t.Errorf("log_path = %q, want %q", got, p.RunDir("run-1"))
	}
	if !filepath.IsAbs(got) {
		t.Errorf("log_path = %q, want an absolute path", got)
	}

	if d := filepath.Dir(p.EnvelopeLog("run-1")); d != got {
		t.Errorf("envelope log lives in %q, log_path says %q", d, got)
	}
	if d := filepath.Dir(p.NodeLog("run-1", "build")); d != got {
		t.Errorf("node log lives in %q, log_path says %q", d, got)
	}
	if hints, hasHints := inv["hints"].(map[string]string); hasHints {
		if _, buried := hints["log_path"]; buried {
			t.Error("log_path must be a top-level invocation field, not a hint")
		}
	}
}

func TestLocalRunLogDir_PointerBackend(t *testing.T) {
	p := Paths{Root: t.TempDir()}
	if got, want := localRunLogDir(&localLogs{paths: p}, "run-1"), p.RunDir("run-1"); got != want {
		t.Errorf("localRunLogDir(*localLogs) = %q, want %q", got, want)
	}
}

func TestLocalRunLogDir_RelativeHomeRecordsAbsolutePath(t *testing.T) {
	wd := t.TempDir()
	t.Chdir(wd)
	t.Setenv("SPARKWING_HOME", "relative-home")

	p, err := paths.DefaultPaths()
	if err != nil {
		t.Fatalf("DefaultPaths: %v", err)
	}
	if filepath.IsAbs(p.RunDir("run-1")) {
		t.Fatalf("fixture is not exercising the relative case: %q", p.RunDir("run-1"))
	}

	got := localRunLogDir(localLogs{paths: p}, "run-1")
	if !filepath.IsAbs(got) {
		t.Fatalf("log_path = %q, want an absolute path", got)
	}
	if want := filepath.Join(wd, "relative-home", "runs", "run-1"); got != want {
		t.Errorf("log_path = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Errorf("recorded log_path does not exist: %v", err)
	}
}

func TestBuildRunInvocation_NoLogPathWithoutLocalLogs(t *testing.T) {
	inv := buildRunInvocation(Options{Pipeline: "demo"}, "run-1", "", nil)
	if v, ok := inv["log_path"]; ok {
		t.Errorf("log_path must be omitted when the run writes no local logs; got %v", v)
	}
}

func TestLocalRunLogDir_UnwritableRootReportsNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	root := filepath.Join(t.TempDir(), "home")
	var setupErr error
	if runtime.GOOS == "windows" {
		setupErr = os.WriteFile(root, []byte("blocked"), 0o600)
	} else {
		setupErr = os.Mkdir(root, 0o500)
	}
	if err := setupErr; err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if dir := localRunLogDir(localLogs{paths: Paths{Root: root}}, "run-1"); dir != "" {
		t.Errorf("localRunLogDir = %q, want empty when the dir cannot be created", dir)
	}
}
