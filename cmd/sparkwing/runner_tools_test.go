package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runRunnerImageCheck(t *testing.T, manifest string) (string, error) {
	t.Helper()
	tools := filepath.Join(t.TempDir(), "runner-tools")
	if err := os.WriteFile(tools, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "../../bin/check-runner-image.sh")
	if runtime.GOOS == "windows" {
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		bash := filepath.Join(filepath.Dir(git), "bash.exe")
		if _, err := os.Stat(bash); err != nil {
			bash = filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe")
		}
		cmd = exec.Command(bash, "../../bin/check-runner-image.sh")
		tools = filepath.ToSlash(tools)
	}
	cmd.Env = append(os.Environ(), "RUNNER_TOOLS="+tools)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The image check fails on a tool the manifest declares and the image lacks,
// and passes the manifest stage when every declared tool is present.
func TestRunnerImageCheckHoldsTheImageToItsToolManifest(t *testing.T) {
	t.Parallel()

	out, err := runRunnerImageCheck(t, "# comment\nsh\nsparkwing-no-such-tool\n")
	if err == nil || !strings.Contains(out, "declares sparkwing-no-such-tool in build/runner-tools but lacks it") {
		t.Fatalf("image check passed a missing declared tool: err=%v output=%s", err, out)
	}
	if out, _ := runRunnerImageCheck(t, "# comment\n\nsh\n"); strings.Contains(out, "build/runner-tools") {
		t.Fatalf("image check refused a manifest of present tools: %s", out)
	}
}
