package client

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestResolveHostBin_EnvOutranksPath(t *testing.T) {
	t.Setenv(HostBinEnv, "/opt/sparkwing/bin/sparkwing")
	bin, ok := ResolveHostBin()
	if !ok || bin != "/opt/sparkwing/bin/sparkwing" {
		t.Fatalf("ResolveHostBin() = %q, %v; want the env value", bin, ok)
	}
}

func TestResolveHostBin_FallsBackToPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the PATH fixture below builds a unix executable")
	}
	t.Setenv(HostBinEnv, "")
	dir := t.TempDir()
	fake := filepath.Join(dir, "sparkwing")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake sparkwing: %v", err)
	}
	t.Setenv("PATH", dir)
	bin, ok := ResolveHostBin()
	if !ok || bin != fake {
		t.Fatalf("ResolveHostBin() = %q, %v; want %q from PATH", bin, ok, fake)
	}
}

func TestResolveHostBin_NothingResolvesReportsFalse(t *testing.T) {
	t.Setenv(HostBinEnv, "")
	t.Setenv("PATH", t.TempDir())
	if bin, ok := ResolveHostBin(); ok {
		t.Fatalf("ResolveHostBin() = %q on a machine with no sparkwing; want none", bin)
	}
	if _, ok := HostSpawn(); ok {
		t.Fatal("HostSpawn() resolved on a machine with no sparkwing; want none")
	}
}

// A no-takeover client that supersedes the running daemon must share it,
// not drain it: it cannot host a successor, so the drain would hand the
// machine's admission to whatever the installed sparkwing happens to be,
// and two pins on one box could drain each other's daemon in a loop.
func TestEnsureDaemon_NoTakeoverSharesSupersededDaemon(t *testing.T) {
	home := shortHome(t)
	var spawns atomic.Int32
	inProcess := spawnInProcess(t, home)
	spawn := func(h, v string) error {
		spawns.Add(1)
		return inProcess(h, v)
	}
	cl, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v2.0.0", // supersedes the in-process daemon's v1.0.0
		Spawn:       spawn,
		NoTakeover:  true,
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon: %v", err)
	}
	defer cl.Close()
	if got := cl.DaemonVersion(); got != "v1.0.0" {
		t.Fatalf("daemon version %q after a no-takeover connect, want the original v1.0.0", got)
	}
	if cl.Draining() {
		t.Fatal("a no-takeover client drained the daemon it was supposed to share")
	}
	if n := spawns.Load(); n != 1 {
		t.Fatalf("spawn fired %d times, want exactly the initial bring-up", n)
	}
}

// oneShotDaemon answers a single handshake with the given ack and then
// keeps the connection open, which is enough to drive a connect decision.
func oneShotDaemon(t *testing.T, home string, ack wingwire.HelloAck) {
	t.Helper()
	sock, err := wingd.SocketPath(home)
	if err != nil {
		t.Fatalf("socket path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer nc.Close()
				r := newFrameReader(nc)
				if _, err := r.read(); err != nil {
					return
				}
				line, _ := wingwire.Encode(&ack)
				if _, err := nc.Write(line); err != nil {
					return
				}
				_, _ = r.read()
			}()
		}
	}()
}

// A no-takeover client facing a daemon whose protocol is older than it
// speaks must fail with advice aimed at the daemon's binary -- the
// installed sparkwing -- rather than attempt a replacement it cannot
// perform, and must spend no takeover budget doing so.
func TestEnsureDaemon_NoTakeoverDaemonProtocolTooOldFails(t *testing.T) {
	home := shortHome(t)
	oneShotDaemon(t, home, wingwire.HelloAck{
		ProtocolMajor:       wingd.ProtocolMajor - 1,
		NativeProtocolMajor: wingd.ProtocolMajor - 1,
		BinaryVersion:       "v0.9.0",
	})

	spawned := make(chan struct{}, 1)
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v2.0.0",
		Spawn:       func(string, string) error { spawned <- struct{}{}; return nil },
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
	})
	if !errors.Is(err, ErrDaemonTooOld) {
		t.Fatalf("error %v does not match ErrDaemonTooOld", err)
	}
	if errors.Is(err, ErrTakeoverExhausted) {
		t.Fatal("a client that never takes over reported an exhausted takeover budget")
	}
	msg := err.Error()
	minVersion, ok := wingwire.ReleasedProtocolFloors().MinVersionSpeaking(wingd.ProtocolMajor)
	if !ok {
		t.Fatalf("no released floor for protocol %d", wingd.ProtocolMajor)
	}
	for _, want := range []string{"v0.9.0", "v2.0.0", minVersion, "daemon restart"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
	select {
	case <-spawned:
		t.Fatal("a no-takeover client spawned a successor for a too-old daemon")
	default:
	}
}

// With no daemon running and a spawn that declares no host exists, the
// connect loop must surface ErrNoDaemonHost bare, so callers can choose to
// run without local coordination instead of failing the run. Reporting it
// as a spawn failure -- with some earlier daemon's log tail attached --
// would send the reader after a process that is not the obstacle.
func TestEnsureDaemon_NoHostSentinelSurfaces(t *testing.T) {
	home := shortHome(t)
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       NoHostSpawn,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(err, ErrNoDaemonHost) {
		t.Fatalf("error %v does not match ErrNoDaemonHost", err)
	}
	if errors.Is(err, ErrDaemonUnreachable) {
		t.Fatal("a declared absence of any host was reported as an unreachable daemon")
	}
}

// A no-takeover client with no Spawn wired must not fall back to the
// self-exec default: that default re-execs this binary as `wingd
// supervise`, and a no-takeover client is by definition one that does not
// serve those verbs.
func TestEnsureDaemon_NoTakeoverNeverSelfExecs(t *testing.T) {
	home := shortHome(t)
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(err, ErrNoDaemonHost) {
		t.Fatalf("error %v does not match ErrNoDaemonHost; an unwired no-takeover client fell back to self-exec", err)
	}
}

// The hosting binaries keep the behavior this feature removes from
// pipeline binaries: sparkwing-runner's in-process client leaves
// NoTakeover unset, and must still drain a daemon its build supersedes and
// bring up its own as the successor.
func TestEnsureDaemon_HostingClientStillTakesOver(t *testing.T) {
	home := shortHome(t)
	oneShotDaemon(t, home, wingwire.HelloAck{
		ProtocolMajor:       wingd.ProtocolMajor,
		NativeProtocolMajor: wingd.ProtocolMajor,
		BinaryVersion:       "v0.1.0",
	})
	spawns := &atomic.Int64{}
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v9.9.9",
		Spawn:       func(string, string) error { spawns.Add(1); return nil },
		DialTimeout: 200 * time.Millisecond,
		Backoff:     5 * time.Millisecond,
	})
	// The fixture never yields, so the budget is what ends this -- but only
	// after the takeovers were actually attempted, which is the point.
	if !errors.Is(err, ErrTakeoverExhausted) {
		t.Fatalf("hosting client ended with %v, want the exhausted-takeover report", err)
	}
	if got := spawns.Load(); got != int64(maxTakeoverAttempts) {
		t.Fatalf("successor spawns = %d, want %d -- the hosting client stopped taking over", got, maxTakeoverAttempts)
	}
}
