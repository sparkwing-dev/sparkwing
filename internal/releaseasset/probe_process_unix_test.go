//go:build !windows

package releaseasset

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestIdentityProbeTerminatesADescendantHoldingOutputPipes(t *testing.T) {
	target := Target{Binary: SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name, err := target.Name()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	body := []byte(fmt.Sprintf(`#!/bin/sh
/bin/sleep 30 &
printf '%%s' "$!" > %q
printf '{"binary":"sparkwing-runner","version":"v1.2.3","goos":"%s","goarch":"%s"}\n'
`, pidFile, runtime.GOOS, runtime.GOARCH))
	digest := sha256.Sum256(body)
	manifest := []byte(hex.EncodeToString(digest[:]) + "  " + name + "\n")
	publicKey, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := Verify([]ed25519.PublicKey{publicKey}, manifest,
		ed25519.Sign(privateKey, manifest), target, body, ed25519.Sign(privateKey, body))
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if _, err := asset.VerifyExecutableIdentity(target, "v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed >= probeTimeout {
		t.Fatalf("identity probe took %s despite an exited leader", elapsed)
	}
	body, err = os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
	}
	assertProcessGone(t, pid)
}

func TestIdentityProbeCancellationTerminatesItsProcessGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe")
	body := []byte(`#!/bin/sh
/bin/sleep 30 &
printf '%s\n' "$!"
wait
`)
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := exec.CommandContext(ctx, path)
	command.Env = []string{}
	ready := make(chan string, 1)
	var stderr bytes.Buffer
	command.Stdout = &probePIDOutput{ready: ready}
	command.Stderr = &stderr
	command.WaitDelay = 100 * time.Millisecond
	finished := make(chan struct{})
	var runErr error
	go func() {
		runErr = runProbeProcess(ctx, command, command.WaitDelay)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		<-finished
	})
	var announcedPID string
	select {
	case announcedPID = <-ready:
	case <-finished:
		t.Fatalf("probe exited before readiness: %v; stderr: %q", runErr, stderr.String())
	case <-t.Context().Done():
		t.Fatal("test ended before probe readiness")
	}
	pid, err := strconv.Atoi(announcedPID)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("inspect live probe descendant: %v", err)
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("probe cleanup did not finish after cancellation")
	}
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("probe error = %v, want cancellation", runErr)
	}
	assertProcessGone(t, pid)
}

type probePIDOutput struct {
	buffer bytes.Buffer
	ready  chan<- string
}

func (output *probePIDOutput) Write(body []byte) (int, error) {
	n, err := output.buffer.Write(body)
	if output.ready != nil {
		if line, _, complete := strings.Cut(output.buffer.String(), "\n"); complete {
			output.ready <- line
			output.ready = nil
		}
	}
	return n, err
}

func TestIdentityProbeExpiredDeadlinePreventsProcessStart(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sleep", "30")
	err := runProbeProcess(ctx, command, 100*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe error = %v, want expired deadline", err)
	}
	if command.Process != nil {
		t.Fatal("probe started with an expired deadline")
	}
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("inspect probe descendant %d: %v", pid, err)
		}
		if time.Now().After(deadline) {
			if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				t.Errorf("kill remaining probe descendant %d: %v", pid, err)
			}
			t.Fatalf("probe descendant %d remained after process-group cleanup", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
