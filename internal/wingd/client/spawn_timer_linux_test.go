package client

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/testmanager"

	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
)

func TestServiceDaemonLeavesCallerCgroup(t *testing.T) {
	testmanager.Require(t)
	if err := procenv.ControlCommandContext(t.Context(), os.Environ(), "systemctl", "--user", "show-environment").Run(); err != nil {
		t.Skip("systemd user manager unavailable")
	}
	t.Setenv("INVOCATION_ID", "timer-test")
	home := shortHome(t)
	bin := filepath.Join(home, "host")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n/bin/cat /proc/self/cgroup > \"$4/cgroup\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		t.Fatal(err)
	}
	if err := spawnDetached(bin, home, ""); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(home, "cgroup"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Fatalf("timer-started daemon retained caller cgroup: %s", after)
	}
}

func TestServiceDaemonRefusesUnsafeFallback(t *testing.T) {
	for _, mode := range []string{"outside-systemd", "missing-manager", "unavailable-bus"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", "timer-test")
			home := shortHome(t)
			bin := filepath.Join(home, "host")
			if err := os.WriteFile(bin, []byte("#!/bin/sh\n/bin/cat /proc/self/cgroup > \"$4/cgroup\"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "outside-systemd":
				t.Setenv("INVOCATION_ID", "")
			case "missing-manager":
				t.Setenv("PATH", home)
			case "unavailable-bus":
				testmanager.Fail(t, "systemd-run")
				testmanager.Fail(t, "systemctl")
				t.Setenv("XDG_RUNTIME_DIR", home)
				t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(home, "missing-bus"))
			}
			err := spawnDetached(bin, home, "")
			if mode != "outside-systemd" {
				if err == nil {
					t.Fatal("daemon fell back into a service cgroup after scope creation failed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile("/proc/self/cgroup")
			if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(filepath.Join(home, "cgroup"))
			if err != nil || string(before) != string(after) {
				t.Fatalf("direct daemon ownership changed: %s, %v", after, err)
			}
		})
	}
}

func TestServiceDaemonNamesAnUnreachableUserManager(t *testing.T) {
	t.Setenv("INVOCATION_ID", "timer-test")
	home := shortHome(t)
	bin := filepath.Join(home, "host")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	const refusal = "Failed to connect to bus: No such file or directory"
	testmanager.FailWith(t, "systemd-run", refusal)
	testmanager.FailWith(t, "systemctl", refusal)
	err := spawnDetached(bin, home, "")
	var failure *apierr.Error
	if !errors.As(err, &failure) || failure.Message != "systemd-run --user --scope: the systemd user manager is unreachable ("+refusal+")" {
		t.Fatalf("scoped start without a user manager = %v", err)
	}
	for _, text := range []string{"INVOCATION_ID", "systemctl --user status", "loginctl enable-linger"} {
		if !strings.Contains(failure.Hint, text) {
			t.Fatalf("hint %q lacks %q", failure.Hint, text)
		}
	}
}
