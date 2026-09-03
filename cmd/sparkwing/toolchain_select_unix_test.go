//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const toolchainExecHelperEnv = "SPARKWING_TOOLCHAIN_EXEC_HELPER"

const toolchainFixtureScript = `#!/bin/sh
echo "fixture argv: $@"
echo "fixture active: $SPARKWING_TOOLCHAIN_ACTIVE"
exit 7
`

// TestToolchainExecHelper is the child half of TestRunToolchainExecsTheStoredCLI:
// it execs the toolchain binary, which replaces this process.
func TestToolchainExecHelper(t *testing.T) {
	if os.Getenv(toolchainExecHelperEnv) == "" {
		t.Skip("child half of TestRunToolchainExecsTheStoredCLI")
	}
	err := runToolchain(os.Stderr, toolchainDecision{action: toolchainSwitch, installed: "v0.38.2", pin: "v9.9.9"})
	fmt.Fprintln(os.Stderr, "exec returned:", err)
	os.Exit(3)
}

func TestRunToolchainExecsTheStoredCLI(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "toolchains", "v9.9.9")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binPath := filepath.Join(dir, "sparkwing")
	if err := os.WriteFile(binPath, []byte(toolchainFixtureScript), 0o755); err != nil {
		t.Fatal(err)
	}
	digest, err := sha256OfFile(binPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath+".sha256", []byte(digest+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// #nosec G702 -- the test binary re-running one of its own tests as the child half
	cmd := exec.Command(os.Args[0], "-test.run=^TestToolchainExecHelper$")
	cmd.Env = append(os.Environ(), toolchainExecHelperEnv+"=1", "SPARKWING_HOME="+home)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run child: %v (stderr %s)", err, stderr.String())
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7 from the fixture (stdout %q, stderr %q)", code, out, stderr.String())
	}
	if !strings.Contains(string(out), "fixture argv: -test.run=^TestToolchainExecHelper$") {
		t.Errorf("the toolchain did not receive the original argv: %q", out)
	}
	if !strings.Contains(string(out), "fixture active: v9.9.9") {
		t.Errorf("the toolchain did not receive the recursion guard: %q", out)
	}
	notice := "sparkwing: running v9.9.9 from " + tildePath(binPath) +
		" because this repo pins SDK v9.9.9 and the installed sparkwing is v0.38.2"
	if !strings.Contains(stderr.String(), notice) {
		t.Errorf("stderr %q does not carry the switch notice %q", stderr.String(), notice)
	}
	if strings.Contains(string(out), "sparkwing: running") {
		t.Error("the switch notice reached stdout")
	}
}
