//go:build !windows

package gitcmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestOutputCancellationKeepsContextSentinel(t *testing.T) {
	if os.Getenv("SPARKWING_TEST_GIT_OUTPUT") == "1" {
		if os.Args[len(os.Args)-1] == "failure" {
			os.Exit(23)
		}
		ready := os.NewFile(3, "ready")
		if _, err := fmt.Fprintln(ready, "ready"); err != nil {
			os.Exit(24)
		}
		_ = ready.Close()
		_, _ = os.Stdin.Read(make([]byte, 1))
		return
	}
	t.Run("cancel running process", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		ready, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer ready.Close()
		defer writer.Close()
		input, keepOpen, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		defer keepOpen.Close()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOutputCancellationKeepsContextSentinel$", "--", "wait")
		cmd.Env = append(os.Environ(), "SPARKWING_TEST_GIT_OUTPUT=1")
		cmd.ExtraFiles = []*os.File{writer}
		cmd.Stdin = input
		done := make(chan error, 1)
		go func() { _, err := Output(ctx, cmd, false); done <- err }()
		reached := make(chan string, 1)
		go func() { line, _ := bufio.NewReader(ready).ReadString('\n'); reached <- line }()
		select {
		case line := <-reached:
			if line != "ready\n" {
				t.Errorf("child readiness=%q", line)
			}
		case <-ctx.Done():
			t.Error("child never started")
		}
		cancel()
		err = <-done
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("owned process cancellation lost its sentinel: %v", err)
		}
	})
	t.Run("real exit", func(t *testing.T) {
		cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOutputCancellationKeepsContextSentinel$", "--", "failure")
		cmd.Env = append(os.Environ(), "SPARKWING_TEST_GIT_OUTPUT=1")
		_, err := Output(t.Context(), cmd, false)
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 23 || errors.Is(err, context.Canceled) {
			t.Fatalf("real exit lost: %v", err)
		}
	})
}
