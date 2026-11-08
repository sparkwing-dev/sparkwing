//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A fake `go` that records its own pid and a grandchild's, then waits. It
// stands in for a real compile: the tree the toolchain leaves behind is what
// this test is about, and a real `go build` cannot be paused mid-link.
const fakeGoScript = `#!/bin/sh
if [ "$1" = env ]; then printf 'auto\ngo1.26.6\noff\n'; exit 0; fi
exec 9> "$TEST_COMPILER_LIFETIME"
sleep 600 &
echo $! > "$TEST_GRANDCHILD_PID"
echo $$ > "$TEST_GO_PID"
printf 'ready\n' >&9
wait
`

func TestMaterializeStopsTheToolchainOnTermination(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_COMPILE_INTERRUPT_CHILD") == "1" {
		err := runSelectedHelp(os.Getenv("SPARKWING_TEST_FLEET_COMPILE_DIR"), "ci", runFlags{noUpdate: true})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	t.Parallel()

	scratch := initSnapshotRepo(t)
	pipelineDir := filepath.Join(scratch, ".sparkwing")
	if err := os.MkdirAll(pipelineDir, 0o750); err != nil {
		t.Fatal(err)
	}
	writeSDKFixture(t, pipelineDir, "package main\nfunc main() {}\n")
	writeRepoFile(t, filepath.Join(pipelineDir, "sparkwing.yaml"), "pipelines:\n  - name: ci\n    entrypoint: CI\n")

	runSnapshotGit(t, scratch, "add", ".")
	runSnapshotGit(t, scratch, "commit", "-qm", "fixture")

	binDir := filepath.Join(scratch, "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(fakeGoScript), 0o700); err != nil { //nolint:gosec // an executable stub is the point
		t.Fatal(err)
	}

	goPIDFile := filepath.Join(scratch, "go.pid")
	grandchildPIDFile := filepath.Join(scratch, "grandchild.pid")

	lifetime, keeper := compileLifetimePipe(t, filepath.Join(scratch, "compiler-lifetime"))
	ready := bufio.NewReader(lifetime)
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestMaterializeStopsTheToolchainOnTermination$")
	child.Env = append(scrubbed(os.Environ(),
		"SPARKWING_FLEET", "SPARKWING_GITCACHE_URL"),
		"SPARKWING_TEST_COMPILE_INTERRUPT_CHILD=1",
		"SPARKWING_TEST_FLEET_COMPILE_DIR="+pipelineDir,
		"TEST_GO_PID="+goPIDFile,
		"TEST_COMPILER_LIFETIME="+lifetime.Name(),
		"TEST_GRANDCHILD_PID="+grandchildPIDFile,
		"SPARKWING_HOME="+filepath.Join(scratch, "home"),
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOWORK=off",
	)
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	finished := make(chan struct{})
	go func() { defer close(finished); exited <- child.Wait() }()
	t.Cleanup(func() {
		_ = child.Process.Kill()
		for _, path := range []string{goPIDFile, grandchildPIDFile} {
			if pid, err := readPID(path); err == nil {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		select {
		case <-finished:
		case <-ctx.Done():
			t.Error("CLI did not join after cleanup")
		}
	})

	if line, err := ready.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("compiler did not report readiness: %q, %v", line, err)
	}

	if err := keeper.Close(); err != nil {
		t.Fatal(err)
	}
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatal("the CLI did not exit after SIGTERM")
	}

	if err := lifetime.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if data, err := io.ReadAll(ready); err != nil || len(data) != 0 {
		t.Fatalf("the compiler or its child outlived the CLI: data=%q err=%v", data, err)
	}
}

func scrubbed(env []string, keys ...string) []string {
	out := make([]string, 0, len(env))
	for _, entry := range env {
		drop := false
		for _, key := range keys {
			if strings.HasPrefix(entry, key+"=") {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

func compileLifetimePipe(t *testing.T, path string) (*os.File, *os.File) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pipe.Close() })
	if err := pipe.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		t.Fatal(err)
	}
	keeper, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = keeper.Close() })
	return pipe, keeper
}

func readPID(path string) (int, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(body)))
}
