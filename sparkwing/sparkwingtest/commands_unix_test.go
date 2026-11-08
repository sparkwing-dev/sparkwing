//go:build unix

package sparkwingtest_test

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/internal/testshell"
	"github.com/sparkwing-dev/sparkwing/sparkwing"
	"github.com/sparkwing-dev/sparkwing/sparkwing/docker"
	"github.com/sparkwing-dev/sparkwing/sparkwing/services"
	"github.com/sparkwing-dev/sparkwing/sparkwing/sparkwingtest"
)

func TestFakeHostCommandsPreserveCaptureFailureAndCancellation(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{Secrets: map[string]sparkwingtest.Secret{"TOKEN": {Value: "unit-secret", Masked: true}}})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx := host.Context(t.Context())
	output, err := sparkwing.Bash(ctx, `printf '\377\000bytes'`).String()
	if err != nil || output != "\xff\x00bytes" {
		t.Fatalf("binary output=%q err=%v", output, err)
	}
	_, err = sparkwing.Bash(ctx, "printf failure >&2; exit 7").Capture()
	var failure *sparkwing.ExecError
	if !errors.As(err, &failure) || failure.ExitCode != 7 || failure.Stderr != "failure" {
		t.Fatalf("failure=%v", err)
	}
	if _, err := sparkwing.Bash(ctx, "printf unit-secret").Run(); err != nil {
		t.Fatal(err)
	}
	for _, record := range host.Records() {
		if strings.Contains(record.Msg, "unit-secret") {
			t.Fatal("command log exposed a secret")
		}
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := sparkwing.Bash(cancelCtx, "exit 0").Run(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled command: %v", err)
	}
	_, err = sparkwing.Exec(ctx, "sparkwing-test-missing-command").Capture()
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing command: %v", err)
	}
}

func TestFakeHostDockerRunUsesHostResources(t *testing.T) {
	dir := t.TempDir()
	testshell.Install(t, filepath.Join(dir, "docker"), "#!/bin/sh\ncase \"$1\" in start) printf container-output;; esac\n")
	t.Setenv("PATH", dir)
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := docker.Run(host.Context(t.Context()), docker.RunOptions{Image: "fixture"}); err != nil {
		t.Fatal(err)
	}
	if err := services.WithServicesAddrs(host.Context(t.Context()), []services.Service{{Name: "fixture-sidecar", Image: "fixture", Port: 1234, ReadyCmd: "true"}}, func(_ context.Context, addrs []services.Addr) error {
		if len(addrs) != 1 || addrs[0].Name != "fixture-sidecar" || addrs[0].HostPort != 1234 {
			t.Fatalf("sidecar addresses=%+v", addrs)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, record := range host.Records() {
		found = found || record.Msg == "container-output"
	}
	if !found {
		t.Fatal("container output absent")
	}
}

func TestFakeHostCloseEndsBackgroundCommandSessions(t *testing.T) {
	host := sparkwingtest.New(sparkwingtest.Options{})
	t.Cleanup(func() {
		if err := host.Close(); err != nil {
			t.Error(err)
		}
	})
	raw, err := sparkwing.Bash(host.Context(t.Context()), "sleep 300 >/dev/null 2>&1 & echo $!").String()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatal(err)
	}
	live := func() bool {
		t.Helper()
		processes, err := procgroup.ListSessions()
		if err != nil {
			t.Fatal(err)
		}
		for _, process := range processes {
			if process.PID == pid && !process.Terminated() {
				return true
			}
		}
		return false
	}
	if !live() {
		t.Fatal("background process ended before host Close")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if live() {
		t.Fatal("background process survived host Close")
	}
}
