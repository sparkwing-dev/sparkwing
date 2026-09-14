//go:build !windows

package main

import (
	"os"
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
sleep 600 &
echo $! > "$SPARKWING_TEST_GRANDCHILD_PID"
echo $$ > "$SPARKWING_TEST_GO_PID"
wait
`

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

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if pid, err := readPID(path); err == nil {
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s never named a pid", path)
	return 0
}

func readPID(path string) (int, error) {
	body, err := os.ReadFile(path) // #nosec G304 -- a path this test wrote
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(body)))
}

func assertGone(t *testing.T, what string, pid int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("%s (pid %d) outlived the CLI it was compiling for", what, pid)
}
