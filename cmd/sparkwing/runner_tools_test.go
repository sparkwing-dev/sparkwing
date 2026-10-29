package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func runRunnerImageCheck(t *testing.T, manifest string, env ...string) (string, error) {
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
	cmd.Env = append(append(os.Environ(), "RUNNER_TOOLS="+tools), env...)
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

// A Go toolchain built for the other architecture still runs under emulation,
// so the image check compares the toolchain's own architecture to the image's.
func TestRunnerImageCheckRefusesAGoToolchainForAnotherArchitecture(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the runner image is linux; the fake toolchain is a shell script")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\necho sparc64\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	out, err := runRunnerImageCheck(t, "sh\n", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err == nil || !strings.Contains(out, "bundles a sparc64 Go toolchain") {
		t.Fatalf("image check accepted a foreign toolchain: err=%v output=%s", err, out)
	}
}
