//go:build !windows

package main

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
)

const soakguardHelper = "SPARKWING_SOAKGUARD_HELPER"

func TestSoakguardHelperProcess(t *testing.T) {
	switch os.Getenv(soakguardHelper) {
	case "nested":
		procgroup.IgnoreTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "leader":
		child := exec.Command(os.Args[0], "-test.run=^TestSoakguardHelperProcess$")
		child.Env = append(os.Environ(), soakguardHelper+"=nested")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		procgroup.IgnoreTermination()
		time.Sleep(30 * time.Second)
		os.Exit(0)
	case "leader-fail":
		child := exec.Command(os.Args[0], "-test.run=^TestSoakguardHelperProcess$")
		child.Env = append(os.Environ(), soakguardHelper+"=nested")
		child.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		time.Sleep(100 * time.Millisecond)
		os.Exit(7)
	}
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
	go func() { done <- run(cmd, signals, started) }()
	session := <-started
	signals <- syscall.SIGTERM
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("exit code = %d, want 130", code)
		}
	case <-time.After(7 * time.Second):
		t.Fatal("soakguard did not bound signal cleanup")
	}
	assertSessionEmpty(t, session)
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
