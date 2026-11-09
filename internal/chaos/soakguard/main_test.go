//go:build !windows

package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/procgroup"
	"github.com/sparkwing-dev/sparkwing/sparkwing/testsupport/testhome"
)

const (
	soakguardHelper      = "SPARKWING_SOAKGUARD_HELPER"
	soakguardLeaderReady = "SPARKWING_SOAKGUARD_LEADER_READY"
)

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
		ready, err := net.Dial("unix", os.Getenv(soakguardLeaderReady))
		if err != nil {
			os.Exit(2)
		}
		_ = ready.Close()
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
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	signals := make(chan os.Signal, 1)
	started := make(chan int, 1)
	done := make(chan int, 1)
	cmd := []string{os.Args[0], "-test.run=^TestSoakguardHelperProcess$"}
	t.Setenv(soakguardHelper, "leader-fail")
	go func() { done <- run(cmd, signals, started) }()
	session := <-started
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	select {
	case code := <-done:
		if code != 7 {
			t.Fatalf("exit code = %d, want 7", code)
		}
	case <-ctx.Done():
		t.Fatal("soakguard did not bound failed-command cleanup")
	}
	assertSessionHasNoLiveProcesses(t, session)
}

func TestSignalTerminatesEveryNestedProcessGroupBeforeExit(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 0.3s of real work; the fast class runs under -short")
	}
	signals := make(chan os.Signal, 1)
	started := make(chan int, 1)
	done := make(chan int, 1)
	cmd := []string{os.Args[0], "-test.run=^TestSoakguardHelperProcess$"}
	t.Setenv(soakguardHelper, "leader")
	readyPath := filepath.Join(testhome.TempDir(t), "leader-ready.sock")
	ready, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: readyPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ready.Close() })
	t.Setenv(soakguardLeaderReady, readyPath)
	go func() { done <- run(cmd, signals, started) }()
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { signals <- syscall.SIGTERM }) }
	joined := false
	t.Cleanup(func() {
		stop()
		if joined {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
		defer cancel()
		select {
		case <-done:
		case <-ctx.Done():
			t.Error("soakguard did not stop during cleanup")
		}
	})
	session := <-started
	if err := ready.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	connection, err := ready.AcceptUnix()
	if err != nil {
		t.Fatalf("soakguard leader readiness: %v", err)
	}
	_ = connection.Close()
	stop()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	select {
	case code := <-done:
		joined = true
		if code != 130 {
			t.Fatalf("exit code = %d, want 130", code)
		}
	case <-ctx.Done():
		t.Fatal("soakguard did not bound signal cleanup")
	}
	assertSessionHasNoLiveProcesses(t, session)
}

func assertSessionHasNoLiveProcesses(t *testing.T, session int) {
	t.Helper()
	processes, err := procgroup.ListSessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, process := range processes {
		if process.Session == session && !process.Terminated() {
			t.Fatalf("session %d retained live pid %d in group %d", session, process.PID, process.Group)
		}
	}
}
