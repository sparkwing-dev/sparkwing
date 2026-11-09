package client

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/pkg/gitenv/testgit"

	"golang.org/x/mod/semver"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

func TestSpawnUnstartableHostNamesTheExecutable(t *testing.T) {
	home := shortHome(t)
	missing := filepath.Join(t.TempDir(), "sparkwign")
	spawn := func(home, version string) error { return spawnDetached(missing, home, version, false) }
	err := spawn(home, "")
	if !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("spawn error = %v, want missing executable %s", err, missing)
	}

	_, cerr := EnsureDaemon(context.Background(), Options{
		Home:        home,
		Spawn:       spawn,
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(cerr, os.ErrNotExist) || !strings.Contains(cerr.Error(), missing) {
		t.Fatalf("connect error = %v, want missing executable %s", cerr, missing)
	}
	if errors.Is(cerr, errDaemonUnreachable) {
		t.Fatal("a bad host binary was reported as an unreachable daemon")
	}
}

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
	spawn := func(home, version string) error { return spawnDetached(bin, home, version, false) }

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err := EnsureDaemon(ctx, Options{
		Home:        home,
		Spawn:       spawn,
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(err, errDaemonHostFailed) {
		t.Fatalf("error %v does not match errDaemonHostFailed", err)
	}
	for _, want := range []string{bin, "unknown subcommand"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q omits %q -- it must name the binary and carry the host's reason", err.Error(), want)
		}
	}
}

func TestSpawn_FailedStartLeavesNoEmptyDaemonLog(t *testing.T) {
	home := shortHome(t)
	logPath, err := wingd.LogPath(home)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	if serr := spawnDetached(filepath.Join(t.TempDir(), "not-a-binary"), home, "", false); serr == nil {
		t.Fatal("spawning a nonexistent binary succeeded")
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("a spawn that never started a process left a daemon log at %s", logPath)
	}
}

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
		Version:     "v2.0.0",
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

func oneShotDaemon(t *testing.T, home string, ack wingwire.HelloAck) <-chan string {
	t.Helper()
	requests := make(chan string, 8)
	serveHTTPFixture(t, home, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == controllerPath+"probe" {
			writeHTTPFixture(w, ack)
			requests <- ""
			return
		}
		requests <- r.URL.Path
		http.NotFound(w, r)
	}))
	return requests
}

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
	if !errors.Is(err, errDaemonTooOld) {
		t.Fatalf("error %v does not match errDaemonTooOld", err)
	}
	msg := err.Error()
	for _, want := range []string{"v0.9.0", "v2.0.0", minHostingRelease(), "daemon restart"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q omits %q", msg, want)
		}
	}

	if strings.Contains(msg, "SPARKWING_HOME") {
		t.Errorf("message %q suggests an escape that does not escape anything", msg)
	}
	select {
	case <-spawned:
		t.Fatal("a no-takeover client spawned a successor for a too-old daemon")
	default:
	}
}

func TestMinHostingRelease_TakesTheHigherOfBothBars(t *testing.T) {
	got := minHostingRelease()
	if semver.Compare(got, firstHostingRelease) < 0 {
		t.Errorf("minHostingRelease() = %s, below the first hosting-capable release %s", got, firstHostingRelease)
	}
	if floor, ok := wingwire.ReleasedProtocolFloors().MinVersionSpeaking(wingd.ProtocolMajor); ok {
		if semver.Compare(got, floor) < 0 {
			t.Errorf("minHostingRelease() = %s, below the protocol floor %s for major %d", got, floor, wingd.ProtocolMajor)
		}
	}
	if !semver.IsValid(firstHostingRelease) {
		t.Errorf("firstHostingRelease %q is not a valid semver; the advice would name a version that does not exist", firstHostingRelease)
	}
}

func TestFirstHostingRelease_NamesAHostingCapableRelease(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available; the release pipeline carries the same check")
	}
	root := repoRootForTest(t)
	cmd := testgit.Command("git", "tag", "--list", firstHostingRelease)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("git tag --list: %v (not a checkout with tags; the release pipeline carries the same check)", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return
	}
	const marker = "internal/wingd/supervise/supervise.go"
	probe := testgit.Command("git", "cat-file", "-e", firstHostingRelease+":"+marker)
	probe.Dir = root
	if err := probe.Run(); err != nil {
		t.Fatalf("firstHostingRelease names %s, a released tag that does not contain %s -- that build cannot host a "+
			"daemon, so every error message naming it as the minimum sends operators to a release that will not help. "+
			"Point the constant at the release that actually ships daemon hosting.", firstHostingRelease, marker)
	}
}

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

func TestEnsureDaemon_NoTakeoverNeverSelfExecs(t *testing.T) {
	home := shortHome(t)
	_, err := EnsureDaemon(context.Background(), Options{
		Home:        home,
		NoTakeover:  true,
		DialTimeout: 200 * time.Millisecond,
		Backoff:     10 * time.Millisecond,
	})
	if !errors.Is(err, errNoDaemonHost) {
		t.Fatalf("error %v does not match errNoDaemonHost; an unwired no-takeover client fell back to self-exec", err)
	}
}
