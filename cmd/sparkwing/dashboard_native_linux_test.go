//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestServeNativeLifecyclePreservesRunningArtifactAndOptions(t *testing.T) {
	source := buildSubmitCLI(t)
	root := t.TempDir()
	binary := filepath.Join(root, "sparkwing")
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(binary, original, 0o700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(root, "state")
	environment := []string{"HOME=" + root, "SPARKWING_HOME=" + state, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"), "PATH=/usr/bin:/bin", "SPARKWING_TOOLCHAIN=local", "SPARKWING_WINGD_BIN=" + binary, "NO_COLOR=1"}
	run := func(args ...string) (dashboardResult, string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, binary, append([]string{"serve"}, args...)...)
		command.Env = environment
		command.Dir = root
		var stderr bytes.Buffer
		command.Stderr = &stderr
		body, e := command.Output()
		var result dashboardResult
		if len(body) > 0 {
			if decodeErr := json.Unmarshal(body, &result); decodeErr != nil {
				t.Fatalf("invalid native receipt: %s %v", body, decodeErr)
			}
		}
		return result, stderr.String(), e
	}
	defer func() {
		result, stderr, e := run("stop")
		if e != nil {
			t.Errorf("cleanup stop: %+v %v %s", result, e, stderr)
		}
	}()
	first, stderr, err := run("start", "--addr", "127.0.0.1:0", "--read-only")
	if err != nil {
		t.Fatalf("start: %v %s", err, stderr)
	}
	if first.Readiness != "ready" || first.Build.Status != "match" || first.PID == 0 {
		t.Fatalf("start: %+v", first)
	}
	replacement := filepath.Join(root, "replacement")
	if err = os.WriteFile(replacement, append(original, byte('\n')), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, binary); err != nil {
		t.Fatal(err)
	}
	second, stderr, err := run("start", "--addr", "127.0.0.1:54321", "--read-only=false")
	if err != nil {
		t.Fatalf("repeat: %v %s", err, stderr)
	}
	if second.PID != first.PID || second.Bind != first.Bind || !second.ReadOnly || second.Build.Status != "different" || second.Build.Running.SHA256 != first.Build.Running.SHA256 {
		t.Fatalf("running instance changed or relabeled: %+v", second)
	}
	observed, stderr, err := run("status")
	if err != nil {
		t.Fatalf("status: %v %s", err, stderr)
	}
	if observed.API != second.API || observed.URL != second.URL || observed.Bind != second.Bind {
		t.Fatalf("status endpoint drift: %+v", observed)
	}
	third, stderr, err := run("restart")
	if err != nil {
		t.Fatalf("restart: %v %s", err, stderr)
	}
	if third.PID == first.PID || third.Bind != first.Bind || !third.ReadOnly || third.Build.Status != "match" {
		t.Fatalf("restart: %+v", third)
	}
	if err = syscall.Kill(third.PID, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	unchanged, stderr, err := run("start")
	if err != nil {
		t.Fatalf("unhealthy repeat: %v %s", err, stderr)
	}
	if unchanged.PID != third.PID || unchanged.Readiness != "not_ready" || unchanged.Outcome != "already_running" {
		t.Fatalf("unhealthy instance replaced: %+v", unchanged)
	}
	stopped, stderr, err := run("stop")
	if err != nil {
		t.Fatalf("forced stop: %v %s", err, stderr)
	}
	if stopped.State != "stopped" || processAlive(third.PID) {
		t.Fatalf("stop left process: %+v", stopped)
	}

	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	blocked, _, e := run("start", "--addr", listener.Addr().String())
	listener.Close()
	if e == nil || blocked.Outcome != "failed" || blocked.State != "stopped" {
		t.Fatalf("occupied port: %+v %v", blocked, e)
	}
	failed, _, e := run("start", "--addr", "127.0.0.1:0", "--log-store", "invalid-scheme://fixture")
	if e == nil || failed.Outcome != "failed" || failed.State != "stopped" {
		t.Fatalf("early exit: %+v %v", failed, e)
	}
	absent, _, err := run("status")
	if err == nil || absent.State != "stopped" {
		t.Fatalf("absent status: %+v %v", absent, err)
	}
}
