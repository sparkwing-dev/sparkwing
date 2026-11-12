//go:build linux

package gotoolchain

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReviewCommandContextWaitDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "trap '' TERM\nprintf 'ready\\n'\nexec /bin/sleep 60")
	cmd.WaitDelay = 50 * time.Millisecond
	cmd.Stdout = writer
	ready := make(chan error, 1)
	go func() {
		_, e := bufio.NewReader(reader).ReadString('\n')
		ready <- e
		cancel()
	}()
	err = Run(ctx, cmd)
	if e := <-ready; e != nil {
		t.Fatal(e)
	}
	var exit *exec.ExitError
	if !errors.Is(err, context.Canceled) || errors.As(err, &exit) {
		t.Fatalf("owner cancellation misclassified: %v", err)
	}
}

func TestRunRetainsPostExitPipeDrainBound(t *testing.T) {
	original := toolchainDescendantProbe
	// safety: isolate exec's pipe bound from the process owner's separate descendant cleanup.
	toolchainDescendantProbe = func(int, bool, bool) (bool, error) { return true, nil }
	t.Cleanup(func() { toolchainDescendantProbe = original })
	pidFile := filepath.Join(t.TempDir(), "holder.pid")
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `/bin/sleep 60 & printf '%s\n' "$!" > "$1"`, "holder", pidFile)
	cmd.Stdout = &bytes.Buffer{}
	cmd.WaitDelay = 50 * time.Millisecond
	err := Run(t.Context(), cmd)
	data, readErr := os.ReadFile(pidFile)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	holder, findErr := os.FindProcess(pid)
	if findErr != nil {
		t.Fatal(findErr)
	}
	defer func() { _ = holder.Kill(); _ = holder.Release() }()
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("post-exit pipe bound lost: %v", err)
	}
}
