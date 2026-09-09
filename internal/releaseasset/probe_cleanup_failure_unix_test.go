//go:build !windows

package releaseasset

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

func TestProbeCleanupFailureStillWaitsForCommand(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
	command.WaitDelay = 100 * time.Millisecond
	group, err := procgroup.StartSession(command)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.ProcessState == nil {
			if err := command.Wait(); err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("wait for test command: %v", err)
			}
		}
	})
	select {
	case <-group.LeaderExited():
	case <-t.Context().Done():
		t.Fatal("command leader did not exit")
	}
	inspectionError := errors.New("fixture descendant inspection failed")
	group.SetDescendantProbe(func(int, bool, bool) (bool, error) { return false, inspectionError })
	err = finishProbeProcess(ctx, command, group, command.WaitDelay)
	if !errors.Is(err, inspectionError) || !errors.Is(err, procgroup.ErrCleanup) {
		t.Fatalf("probe error = %v, want inspection and cleanup causes", err)
	}
	if command.ProcessState == nil {
		t.Error("probe returned without waiting for its command")
	}
}

func TestExecutableIdentityTimeoutPreservesProcessFailure(t *testing.T) {
	target := Target{Binary: SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name, err := target.Name()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\nexec /bin/sleep 30\n")
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
	_, err = asset.VerifyExecutableIdentity(target, "v1.2.3")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("identity error = %v, want deadline exceeded", err)
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("identity error = %v, want process exit cause", err)
	}
}

func TestExecutableIdentityPreservesCleanupFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	target := Target{Binary: SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	name, err := target.Name()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("#!/bin/sh\nprintf residue > \"$0.keep\"\nprintf invalid\n")
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
	_, err = asset.VerifyExecutableIdentity(target, "v1.2.3")
	var syntaxError *json.SyntaxError
	if !errors.As(err, &syntaxError) || !errors.Is(err, syscall.ENOTEMPTY) {
		t.Fatalf("identity error = %v, want JSON and directory cleanup causes", err)
	}
}

func TestProbeNonzeroExitPreservesWaitResult(t *testing.T) {
	command := exec.CommandContext(t.Context(), "/bin/sh", "-c", "exit 7")
	command.WaitDelay = 100 * time.Millisecond
	err := runProbeProcess(t.Context(), command, command.WaitDelay)
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 7 || err.Error() != exitError.Error() {
		t.Fatalf("probe error = %v, want native exit status 7", err)
	}
	if command.ProcessState == nil {
		t.Fatal("probe returned without waiting for the failed command")
	}
}
