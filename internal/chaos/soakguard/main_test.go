//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const soakguardHelper = "SPARKWING_SOAKGUARD_HELPER"
const soakguardLeaderReady = "SPARKWING_SOAKGUARD_LEADER_READY"

func TestSoakguardHelperProcess(t *testing.T) {
	switch os.Getenv(soakguardHelper) {
	case "nested":
		procgroup.IgnoreTermination()
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		release := make(chan os.Signal, 1)
		signal.Notify(release, syscall.SIGUSR1)
		<-release
		os.Exit(0)
	case "leader":
		if !startNestedHelper() {
			os.Exit(2)
		}
		procgroup.IgnoreTermination()
		if err := os.WriteFile(os.Getenv(soakguardLeaderReady), []byte("ready"), 0o600); err != nil {
			os.Exit(2)
		}
		release := make(chan os.Signal, 1)
		signal.Notify(release, syscall.SIGUSR1)
		<-release
		os.Exit(0)
	case "leader-fail":
		if !startNestedHelper() {
			os.Exit(2)
		}
		os.Exit(7)
	}
}

func startNestedHelper() bool {
	child := exec.Command(os.Args[0], "-test.run=^TestSoakguardHelperProcess$")
	child.Env = append(os.Environ(), soakguardHelper+"=nested")
	child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := child.StdoutPipe()
	if err != nil {
		return false
	}
	if err := child.Start(); err != nil {
		return false
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	return err == nil && line == "ready\n"
}

func TestFailedCommandCleansNestedGroupsBeforeReturningStatus(t *testing.T) {
	signals := make(chan os.Signal, 1)
	started := make(chan int, 1)
	done := make(chan int, 1)
	cmd := []string{os.Args[0], "-test.run=^TestSoakguardHelperProcess$"}
	t.Setenv(soakguardHelper, "leader-fail")
	go func() { done <- run(cmd, signals, started) }()
	session := <-started
	select {
	case code := <-done:
		if code != 7 {
			t.Fatalf("exit code = %d, want 7", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("soakguard did not bound failed-command cleanup")
	}
	assertSessionEmpty(t, session)
}

func TestSignalTerminatesEveryNestedProcessGroupBeforeExit(t *testing.T) {
	signals := make(chan os.Signal, 1)
	started := make(chan int, 1)
	done := make(chan int, 1)
	cmd := []string{os.Args[0], "-test.run=^TestSoakguardHelperProcess$"}
	t.Setenv(soakguardHelper, "leader")
	ready := filepath.Join(t.TempDir(), "leader-ready")
	t.Setenv(soakguardLeaderReady, ready)
	go func() { done <- run(cmd, signals, started) }()
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { signals <- syscall.SIGTERM }) }
	joined := false
	t.Cleanup(func() {
		stop()
		if joined {
			return
		}
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("soakguard did not stop during cleanup")
		}
	})
	session := <-started
	waitForSoakguardMarker(t, ready)
	stop()
	select {
	case code := <-done:
		joined = true
		if code != 130 {
			t.Fatalf("exit code = %d, want 130", code)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("soakguard did not bound signal cleanup")
	}
	assertSessionEmpty(t, session)
}

func waitForSoakguardMarker(t *testing.T, path string) {
	t.Helper()
	deadlineAt := time.Now().Add(3 * time.Second)
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(time.Until(deadlineAt))
	defer deadline.Stop()
	for {
		if !time.Now().Before(deadlineAt) {
			t.Fatal("soakguard leader did not publish readiness")
		}
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatalf("read soakguard leader readiness: %v", err)
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("soakguard leader did not publish readiness")
		}
	}
}

func assertSessionEmpty(t *testing.T, session int) {
	t.Helper()
	processes, err := procgroup.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.Session == session {
			t.Fatalf("session %d retained pid %d in group %d", session, process.PID, process.Group)
		}
	}
}
