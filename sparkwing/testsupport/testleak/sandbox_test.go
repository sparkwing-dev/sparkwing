package testleak

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/gitcontrol"
)

const homeProbeEnv = "SPARKWING_TESTLEAK_HOME_PROBE"

var sandboxedVars = []string{"HOME", "SPARKWING_HOME", "XDG_CONFIG_HOME"}

func TestSandboxHomesThisSuiteInsideATempDir(t *testing.T) {
	if sandbox == "" {
		t.Fatal("the suite runs without a sandbox home")
	}
	if !strings.HasPrefix(sandbox, os.TempDir()+string(filepath.Separator)) {
		t.Errorf("sandbox %q is not under %s", sandbox, os.TempDir())
	}
	for _, name := range sandboxedVars {
		if v := os.Getenv(name); !strings.HasPrefix(v, sandbox+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a path inside the sandbox %s", name, v, sandbox)
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home != os.Getenv("HOME") {
		t.Errorf("os.UserHomeDir() = %q, %v; want the sandbox HOME %q", home, err, os.Getenv("HOME"))
	}
	if runtime.GOOS == "windows" && os.Getenv("USERPROFILE") != os.Getenv("HOME") {
		t.Errorf("USERPROFILE = %q, want the sandbox HOME", os.Getenv("USERPROFILE"))
	}
}

// TestHelperReportsItsHome is the payload the home probe runs.
func TestHelperReportsItsHome(t *testing.T) {
	if os.Getenv(homeProbeEnv) == "" {
		t.Skip("runs only in the child process this package spawns")
	}
	for _, name := range sandboxedVars {
		_, _ = os.Stdout.WriteString(name + "=" + os.Getenv(name) + "\n")
	}
}

func TestReExecInheritsTheSandboxAndLeavesItInPlace(t *testing.T) {
	marker := filepath.Join(os.Getenv("HOME"), t.Name())
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperReportsItsHome$")
	cmd.Env = append(os.Environ(), homeProbeEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("home probe: %v\n%s", err, out)
	}
	for _, name := range sandboxedVars {
		if want := name + "=" + os.Getenv(name) + "\n"; !strings.Contains(string(out), want) {
			t.Errorf("a re-exec'd helper did not inherit %q:\n%s", strings.TrimSpace(want), out)
		}
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the sandbox home lost a file after a helper exited: %v", err)
	}
}

func TestGoTelemetryCannotRecreateTheSandboxHome(t *testing.T) {
	cmd := exec.Command("go", "env", "GOTELEMETRYDIR")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go telemetry directory: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != os.DevNull {
		t.Fatalf("Go telemetry directory = %q, want %q", got, os.DevNull)
	}
}

func TestFreshSuiteScrubsAmbientGitBindings(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=^TestHelperRejectsAmbientGitBindings$")
	command.Env = append(gitcontrol.Env(os.Environ()), hostEnv+"=", "SPARKWING_TESTLEAK_GIT_PROBE=1",
		"GIT_DIR="+t.TempDir(), "GIT_WORK_TREE="+t.TempDir(), "GIT_INDEX_FILE="+filepath.Join(t.TempDir(), "index"),
		"GIT_COMMON_DIR="+t.TempDir(), "GIT_OBJECT_DIRECTORY="+t.TempDir(), "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.worktree", "GIT_CONFIG_VALUE_0="+t.TempDir())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("fresh suite Git probe: %v: %s", err, output)
	}
}

func TestHelperRejectsAmbientGitBindings(t *testing.T) {
	if os.Getenv("SPARKWING_TESTLEAK_GIT_PROBE") != "1" {
		return
	}
	for _, name := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		if _, set := os.LookupEnv(name); set {
			t.Errorf("fresh suite inherited %s", name)
		}
	}
}
