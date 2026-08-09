package client

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// blockSocket strips search permission from the directory holding home's
// daemon socket, so a dial fails with EACCES the way a sandbox denial does
// rather than with the ENOENT an idle machine produces.
func blockSocket(t *testing.T, home string) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	dir := filepath.Dir(sock)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatalf("place socket file: %v", err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("chmod socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// TestQuery_UnreachableSocketIsNotAnIdleMachine pins the distinction the two
// sentinels exist to draw. A socket that cannot be reached says nothing about
// what is queued behind it, so it must not come back as the sentinel a quiet
// machine returns.
func TestQuery_UnreachableSocketIsNotAnIdleMachine(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reaches a socket whatever its directory mode")
	}
	home := shortHome(t)
	blockSocket(t, home)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Query(ctx, Options{Home: home, DialTimeout: 200 * time.Millisecond, Backoff: 10 * time.Millisecond})
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Fatalf("Query against a blocked socket: got %v, want ErrDaemonUnreachable", err)
	}
	if errors.Is(err, ErrNoDaemon) {
		t.Errorf("a blocked socket was reported as no daemon running: %v", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error does not name why the socket refused to answer: %v", err)
	}
}

// The negative control for the test above: nothing listening really is an idle
// machine, and it must keep saying so.
func TestQuery_AbsentSocketStaysNoDaemon(t *testing.T) {
	home := shortHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := Query(ctx, Options{Home: home, DialTimeout: 200 * time.Millisecond, Backoff: 10 * time.Millisecond})
	if !errors.Is(err, ErrNoDaemon) {
		t.Fatalf("Query with nothing listening: got %v, want ErrNoDaemon", err)
	}
	if errors.Is(err, ErrDaemonUnreachable) {
		t.Errorf("an idle machine was reported as unreachable: %v", err)
	}
}

// TestProbe_TellsAnUnreachableSocketFromAnAbsentOne keeps doctor's daemon
// section honest: it reads its verdict from Probe, so a probe that collapses
// both into ErrNoDaemon would report "no daemon" for a daemon it could not
// look at.
func TestProbe_TellsAnUnreachableSocketFromAnAbsentOne(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reaches a socket whatever its directory mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	absent := shortHome(t)
	absentSock, err := wingd.SocketPath(absent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, absentSock); !errors.Is(err, ErrNoDaemon) {
		t.Errorf("probe of an unserved socket: got %v, want ErrNoDaemon", err)
	}

	blocked := shortHome(t)
	blockSocket(t, blocked)
	blockedSock, err := wingd.SocketPath(blocked)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Probe(ctx, blockedSock); !errors.Is(err, ErrDaemonUnreachable) {
		t.Errorf("probe of a blocked socket: got %v, want ErrDaemonUnreachable", err)
	}
}

func TestDaemonDeathCause_LeadsWithTheLastLoggedReason(t *testing.T) {
	tail := "wingd: starting\nwingd: state restored\nsparkwing error: wingd: listen /tmp/x/d.sock: bind: operation not permitted\n"
	got := daemonDeathCause(tail)
	if got != "wingd: listen /tmp/x/d.sock: bind: operation not permitted" {
		t.Fatalf("daemonDeathCause = %q, want the last logged reason with its prefix stripped", got)
	}
}

// TestDaemonUnreachable_LeadsWithTheCauseNotTheSymptom pins the message order.
// It used to open with "started but exited before serving", which reads as a
// crash, and left the bind failure buried under eight lines of log tail where
// the reader stopped before reaching it.
func TestDaemonUnreachable_DoesNotClaimAnUnobservedExit(t *testing.T) {
	home := shortHome(t)
	logPath, err := wingd.LogPath(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath,
		[]byte("wingd: starting\nsparkwing error: wingd: listen /tmp/x/d.sock: bind: operation not permitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = daemonUnreachable(home, "/tmp/x/d.sock", 1, errors.New("dial timeout"), nil)
	first, _, _ := strings.Cut(err.Error(), "\n")
	if !strings.Contains(first, "did not become reachable after 1 start attempt") {
		t.Fatalf("first line does not state the observed failure: %q", first)
	}
	if strings.Contains(err.Error(), "exited") {
		t.Fatalf("error claims an unobserved process exit: %v", err)
	}
	if !strings.Contains(err.Error(), logPath) {
		t.Errorf("error dropped the daemon log path %q: %v", logPath, err)
	}
	if !errors.Is(err, ErrDaemonUnreachable) {
		t.Errorf("a daemon that never served did not wrap ErrDaemonUnreachable: %v", err)
	}
}
