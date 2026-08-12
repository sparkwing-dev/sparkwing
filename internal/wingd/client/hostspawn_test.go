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

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestResolveHostBin_EnvOutranksPath(t *testing.T) {
	t.Setenv(HostBinEnv, "/opt/sparkwing/bin/sparkwing")
	bin, fromEnv, ok := ResolveHostBin()
	if !ok || bin != "/opt/sparkwing/bin/sparkwing" || !fromEnv {
		t.Fatalf("ResolveHostBin() = %q, fromEnv=%v, ok=%v; want the env value", bin, fromEnv, ok)
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
	bin, fromEnv, ok := ResolveHostBin()
	if !ok || bin != fake || fromEnv {
		t.Fatalf("ResolveHostBin() = %q, fromEnv=%v, ok=%v; want %q from PATH", bin, fromEnv, ok, fake)
	}
}

func TestResolveHostBin_NothingResolvesReportsFalse(t *testing.T) {
	t.Setenv(HostBinEnv, "")
	t.Setenv("PATH", t.TempDir())
	if bin, _, ok := ResolveHostBin(); ok {
		t.Fatalf("ResolveHostBin() = %q on a machine with no sparkwing; want none", bin)
	}
	if _, ok := HostSpawn(); ok {
		t.Fatal("HostSpawn() resolved on a machine with no sparkwing; want none")
	}
}

// A typo'd or stale $SPARKWING_WINGD_BIN is the common way to get a host
// that cannot start. The error must name the variable and the value,
// because "could not reach the admission daemon" sends the reader to
// inspect a daemon when what is wrong is a path they typed.
func TestHostSpawn_UnstartableHostNamesTheVariableAndValue(t *testing.T) {
	home := shortHome(t)
	missing := filepath.Join(t.TempDir(), "sparkwign") // deliberate typo
	t.Setenv(HostBinEnv, missing)
	spawn, ok := HostSpawn()
	if !ok {
		t.Fatal("HostSpawn() did not resolve an explicitly named binary")
	}
	err := spawn(home, "")
	if !errors.Is(err, ErrDaemonHostUnusable) {
		t.Fatalf("error %v does not match ErrDaemonHostUnusable", err)
	}
	for _, want := range []string{HostBinEnv, missing} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q omits %q", err.Error(), want)
		}
	}

	// And it must reach a caller intact rather than as "unreachable".
	_, cerr := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       spawn,
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(cerr, ErrDaemonHostUnusable) {
		t.Fatalf("connect reported %v, want the unusable-host sentinel", cerr)
	}
	if errors.Is(cerr, ErrDaemonUnreachable) {
		t.Fatal("a bad host binary was reported as an unreachable daemon")
	}
}

// A host binary that starts and immediately exits non-zero -- the shape
// of an installed sparkwing too old to know the verb it was handed --
// must fail the connect promptly, carrying the host's own reason, rather
// than waiting out the full socket budget for a socket that will never
// appear.
func TestSpawn_HostThatExitsImmediatelyFailsFast(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture below is a unix shell script")
	}
	home := shortHome(t)
	bin := filepath.Join(t.TempDir(), "sparkwing")
	script := "#!/bin/sh\necho 'wingd: unknown subcommand \"" + DaemonSpawnVerb + "\"' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fixture host: %v", err)
	}
	t.Setenv(HostBinEnv, bin)
	spawn, ok := HostSpawn()
	if !ok {
		t.Fatal("HostSpawn() did not resolve the fixture")
	}

	start := time.Now()
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       spawn,
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	elapsed := time.Since(start)
	if !errors.Is(err, ErrDaemonHostFailed) {
		t.Fatalf("error %v does not match ErrDaemonHostFailed", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("waited %s for a host that died immediately; the socket budget is ~%s", elapsed, daemonStartupBudget(Options{}))
	}
	for _, want := range []string{bin, "unknown subcommand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q omits %q -- it must name the binary and carry the host's reason", err.Error(), want)
		}
	}
}

// A spawn that never runs must not leave an empty daemon log behind: a
// zero-byte d.log reads as "a daemon ran here and said nothing", which
// is the opposite of what happened.
func TestSpawn_FailedStartLeavesNoEmptyDaemonLog(t *testing.T) {
	home := shortHome(t)
	logPath, err := wingd.LogPath(home)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if serr := spawnDetached(filepath.Join(t.TempDir(), "not-a-binary"), home, ""); serr == nil {
		t.Fatal("spawning a nonexistent binary succeeded")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("a spawn that never started a process left a daemon log at %s", logPath)
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
	for _, want := range []string{"v0.9.0", "v2.0.0", minHostingRelease(), "daemon restart"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}
	// A private home gets its own daemon from the same unusable
	// installation, so suggesting one would send the reader in a circle.
	if strings.Contains(msg, "SPARKWING_HOME") {
		t.Errorf("message %q suggests an escape that does not escape anything", msg)
	}
	select {
	case <-spawned:
		t.Fatal("a no-takeover client spawned a successor for a too-old daemon")
	default:
	}
}

// The release an operator must install is the higher of two bars: new
// enough to speak this client's protocol, and new enough to host a daemon
// at all. Naming only the protocol floor would send them to a build that
// clears the handshake and still cannot serve the spawn verb.
func TestMinHostingRelease_TakesTheHigherOfBothBars(t *testing.T) {
	got := minHostingRelease()
	if semver.Compare(got, FirstHostingRelease) < 0 {
		t.Errorf("minHostingRelease() = %s, below the first hosting-capable release %s", got, FirstHostingRelease)
	}
	if floor, ok := wingwire.ReleasedProtocolFloors().MinVersionSpeaking(wingd.ProtocolMajor); ok {
		if semver.Compare(got, floor) < 0 {
			t.Errorf("minHostingRelease() = %s, below the protocol floor %s for major %d", got, floor, wingd.ProtocolMajor)
		}
	}
	if !semver.IsValid(FirstHostingRelease) {
		t.Errorf("FirstHostingRelease %q is not a valid semver; the advice would name a version that does not exist", FirstHostingRelease)
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
