package client

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
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

// The same fast-fail has to hold on the takeover path, which is worse
// than a cold start: the old daemon has already been drained, so a
// successor that cannot start leaves the box with nothing while the
// client spends its whole takeover budget re-draining and re-spawning.
//
// It is also where "the socket exists" stops meaning "the host is up":
// the predecessor's socket is still in place when the successor starts.
func TestTakeover_BrokenSuccessorFailsFastNamingTheBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fixture below is a unix shell script")
	}
	home := shortHome(t)
	oneShotDaemon(t, home, wingwire.HelloAck{
		ProtocolMajor:       wingd.ProtocolMajor,
		NativeProtocolMajor: wingd.ProtocolMajor,
		BinaryVersion:       "v0.1.0",
	})
	bin := filepath.Join(t.TempDir(), "sparkwing")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho 'wingd: cannot serve' >&2\nexit 1\n"), 0o755); err != nil {
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
		Version:     "v9.9.9", // supersedes the fixture daemon, so this takes over
		Spawn:       spawn,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     5 * time.Millisecond,
	})
	if !errors.Is(err, ErrDaemonHostFailed) {
		t.Fatalf("error %v does not match ErrDaemonHostFailed", err)
	}
	if errors.Is(err, ErrTakeoverExhausted) {
		t.Fatal("a broken successor was reported as a version skew after burning the takeover budget")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited %s for a successor that died immediately", elapsed)
	}
	if !strings.Contains(err.Error(), bin) {
		t.Errorf("message %q does not name the successor binary", err.Error())
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

// A no-takeover client may share a same-source daemon even when their display
// versions differ.
func TestEnsureDaemon_NoTakeoverAcceptsSameSourceDaemon(t *testing.T) {
	home := shortHome(t)
	var spawns atomic.Int32
	inProcess := spawnInProcess(t, home)
	spawn := func(h, v string) error {
		spawns.Add(1)
		return inProcess(h, v)
	}
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Version:     "v2.0.0", // supersedes the in-process daemon's v1.0.0
		Spawn:       spawn,
		NoTakeover:  true,
		DialTimeout: 500 * time.Millisecond,
		Backoff:     20 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("ensure daemon error = %v, want success", err)
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

// TestFirstHostingRelease_NamesAHostingCapableRelease is the live half
// of keeping that constant honest. It names the first release able to
// host a daemon on request, and it can be wrong in two ways: while the
// feature is unreleased, the tag must not exist yet (a tag cut without
// this work would send operators to a build that cannot host); once the
// release ships, the tag must actually contain the hosting code (a
// bumped constant naming some future release would fail the moment that
// release shipped without it). The supervisor package is the marker --
// it is what the spawn verb reaches, so a tag that carries it can host.
//
// The release pipeline carries the pre-release half at tag time. This
// test is what catches both halves on a branch, before anyone releases.
func TestFirstHostingRelease_NamesAHostingCapableRelease(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; the release pipeline carries the same check")
	}
	root := repoRootForTest(t)
	cmd := exec.Command("git", "tag", "--list", FirstHostingRelease)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git tag --list: %v (not a checkout with tags; the release pipeline carries the same check)", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		// Unreleased: nothing to verify until the tag exists, and the
		// release pipeline refuses to cut a mismatched version.
		return
	}
	const marker = "internal/wingd/supervise/supervise.go"
	probe := exec.Command("git", "cat-file", "-e", FirstHostingRelease+":"+marker)
	probe.Dir = root
	if err := probe.Run(); err != nil {
		t.Fatalf("FirstHostingRelease names %s, a released tag that does not contain %s -- that build cannot host a "+
			"daemon, so every error message naming it as the minimum sends operators to a release that will not help. "+
			"Point the constant at the release that actually ships daemon hosting.", FirstHostingRelease, marker)
	}
}

// repoRootForTest walks up to the module root so `git` runs inside the
// checkout regardless of where the test binary was invoked from.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("no module root above the test directory")
		}
		dir = parent
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
