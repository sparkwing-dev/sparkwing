//go:build e2e && !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestCompileAndExecStopsTheToolchainOnTermination(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_COMPILE_INTERRUPT_CHILD") == "1" {
		_ = compileAndExec(os.Getenv("SPARKWING_TEST_FLEET_COMPILE_DIR"), nil,
			append(os.Environ(), "GOWORK=off"), compileOptions{NoUpdate: true})
		os.Exit(0)
	}

	scratch := t.TempDir()
	pipelineDir := filepath.Join(scratch, "pipeline")
	if err := os.MkdirAll(pipelineDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "go.mod"),
		[]byte("module example.test/compileinterrupt\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pipelineDir, "main.go"),
		[]byte("package main\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(scratch, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(fakeGoScript), 0o700); err != nil { //nolint:gosec // an executable stub is the point
		t.Fatal(err)
	}

	goPIDFile := filepath.Join(scratch, "go.pid")
	grandchildPIDFile := filepath.Join(scratch, "grandchild.pid")

	child := exec.Command(os.Args[0], "-test.run=^TestCompileAndExecStopsTheToolchainOnTermination$")
	// safety: three ambient settings route compileAndExec away from the
	// compile this test drives, so they are cleared rather than inherited.
	child.Env = append(scrubbed(os.Environ(),
		"SPARKWING_NO_BINCACHE", "SPARKWING_FLEET", "SPARKWING_GITCACHE_URL"),
		"SPARKWING_TEST_COMPILE_INTERRUPT_CHILD=1",
		"SPARKWING_TEST_FLEET_COMPILE_DIR="+pipelineDir,
		"SPARKWING_TEST_GO_PID="+goPIDFile,
		"SPARKWING_TEST_GRANDCHILD_PID="+grandchildPIDFile,
		"SPARKWING_HOME="+filepath.Join(scratch, "home"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOWORK=off",
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		for _, path := range []string{goPIDFile, grandchildPIDFile} {
			if pid, err := readPID(path); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})

	goPID := waitForPIDFile(t, goPIDFile)
	grandchildPID := waitForPIDFile(t, grandchildPIDFile)

	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(60 * time.Second):
		t.Fatal("the CLI did not exit after SIGTERM")
	}

	assertGone(t, "the toolchain process", goPID)
	assertGone(t, "the toolchain's own child", grandchildPID)
}
