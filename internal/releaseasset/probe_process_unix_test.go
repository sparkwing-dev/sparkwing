//go:build !windows

package releaseasset

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
/bin/sleep 30 &
printf '%%s' "$!" > %q
wait
`, pidFile))
	if err := os.WriteFile(path, body, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = 100 * time.Millisecond
	started := time.Now()
	runErr := runProbeProcess(ctx, cmd, cmd.WaitDelay)
	if runErr == nil {
		t.Fatal("timed-out probe process succeeded")
	}
	if ctx.Err() != context.DeadlineExceeded {
		t.Fatalf("probe context error = %v", ctx.Err())
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Fatalf("timed-out probe cleanup took %s", elapsed)
	}
	body, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read descendant pid: %v (run error: %v; stdout: %q; stderr: %q)", err, runErr, stdout.String(), stderr.String())
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		t.Fatal(err)
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
