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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
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

func TestIdentityProbeTimeoutTerminatesItsProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "descendant.pid")
	path := filepath.Join(t.TempDir(), "probe")
	body := []byte(fmt.Sprintf(`#!/bin/sh
/bin/sleep 2
/bin/sleep 30 &
printf '%%s' "$!" > %q
printf r >&3
wait
`, pidFile))
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	readyReader, readyWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readyReader.Close()
	defer readyWriter.Close()
	ctx, cancel := context.WithCancelCause(context.Background())
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{}
	cmd.ExtraFiles = []*os.File{readyWriter}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = probeWaitDelay
	finished := make(chan struct{})
	var runErr error
	go func() {
		runErr = runProbeProcess(ctx, cmd, cmd.WaitDelay)
		close(finished)
	}()
	defer func() {
		cancel(context.Canceled)
		<-finished
	}()
	ready := make(chan error, 1)
	readerFinished := make(chan struct{})
	go func() {
		defer close(readerFinished)
		var signal [1]byte
		_, err := io.ReadFull(readyReader, signal[:])
		ready <- err
	}()
	defer func() {
		_ = readyReader.Close()
		<-readerFinished
	}()
	startup, stopStartup := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopStartup()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("probe readiness: %v", err)
		}
	case <-finished:
		t.Fatalf("probe exited before readiness: %v", runErr)
	case <-startup.Done():
		t.Fatal("probe did not become ready")
	}
	body, err = os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read descendant pid: %v", err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	timeout, stopTimeout := context.WithTimeout(context.Background(), time.Second)
	defer stopTimeout()
	select {
	case <-finished:
		t.Fatalf("probe exited before timeout: %v", runErr)
	case <-timeout.Done():
		cancel(context.Cause(timeout))
	}
	cleanup, stopCleanup := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopCleanup()
	select {
	case <-finished:
	case <-cleanup.Done():
		t.Fatal("timed-out probe cleanup exceeded three seconds")
	}
	if runErr == nil {
		t.Fatal("timed-out probe process succeeded")
	}
	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		t.Fatalf("probe context cause = %v", context.Cause(ctx))
	}
	if cmd.ProcessState == nil {
		t.Fatalf("timed-out probe leader was not reaped: %v", runErr)
	}
	assertProcessGone(t, pid)
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	defer func() { _ = syscall.Kill(pid, syscall.SIGKILL) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe descendant %d remained after process-group cleanup", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
