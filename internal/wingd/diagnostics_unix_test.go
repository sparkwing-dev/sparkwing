//go:build !windows

package wingd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
)

// TestDiagnosticsDumpsOnSIGUSR1 pins the field-triage hook. A daemon
// burning CPU can only be explained while it is still running, and the
// operator's other option -- killing it -- is what destroys the evidence.
func TestDiagnosticsDumpsOnSIGUSR1(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	d := &Daemon{
		cfg: Config{Version: "v0.0.0-test", Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		}},
		quit: make(chan struct{}),
	}
	done := make(chan struct{})
	defer close(done)
	d.startDiagnostics(done)

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("raise SIGUSR1: %v", err)
	}

	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		mu.Lock()
		got := strings.Join(lines, "\n")
		mu.Unlock()
		if strings.Contains(got, "goroutine dump") {
			return
		}
		select {
		case <-poll.C:
		case <-deadline.C:
			t.Fatal("SIGUSR1 produced no goroutine dump in the daemon log")
		}
	}
}

// seedOversizedLog gives home a daemon log one byte past the cap and
// returns its path.
func seedOversizedLog(t *testing.T, home string) string {
	t.Helper()
	path, err := LogPath(home)
	if err != nil {
		t.Fatalf("log path: %v", err)
	}
	dir, err := StateDir(home)
	if err != nil {
		t.Fatalf("state dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create state dir: %v", err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte("o"), LogCapBytes+1), 0o600); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}
	return path
}

// TestDiagnosticsRotatesAnOversizedLogBeforeDumping pins the bound on a
// daemon that never restarts. Each dump appends up to 2MB and rotation
// used to happen only at spawn, so a resident daemon asked for a handful
// of dumps grew d.log without limit.
//
// The sink stands in for a spawned daemon's output, which the client
// points at d.log before the process starts.
func TestDiagnosticsRotatesAnOversizedLogBeforeDumping(t *testing.T) {
	home := t.TempDir()
	path := seedOversizedLog(t, home)

	sink, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = sink.Close() }()

	d := &Daemon{cfg: Config{
		Home:    home,
		Version: "v0.0.0-test",
		Logf:    func(format string, args ...any) { fmt.Fprintf(sink, format+"\n", args...) },
	}}
	d.writeDiagnosticDump()
	d.writeDiagnosticDump()

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("d.log is gone after rotation: %v", err)
	}
	if fi.Size() > LogCapBytes {
		t.Fatalf("d.log is %d bytes, over the %d cap: the dump did not follow the rotation", fi.Size(), LogCapBytes)
	}
	if fi.Size() == 0 {
		t.Fatal("d.log is empty: the dumps went somewhere other than the rotated-in log")
	}
	archived, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("the oversized log was not rotated to d.log.1: %v", err)
	}
	if archived.Size() <= LogCapBytes {
		t.Fatalf("d.log.1 is %d bytes; it should hold the oversized log", archived.Size())
	}

	dumped, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read d.log: %v", err)
	}
	if got := strings.Count(string(dumped), "goroutine dump"); got != 2 {
		t.Fatalf("d.log holds %d dumps, want both of them:\n%s", got, truncate(string(dumped), 400))
	}
}

// parentMarker is what the holding process writes after the rotation has
// happened in another process.
const parentMarker = "holder-wrote-this-after-the-rotation"

// rotationChildEnv carries the home the child arm rotates.
const rotationChildEnv = "SPARKWING_TEST_ROTATION_HOME"

// TestDiagnosticsRotationFollowsAProcessHoldingAnInheritedLog is the
// shape production actually has, which a single-process test cannot see.
// The client points the supervisor's stdout and stderr at d.log, the
// supervisor hands those same descriptors to every daemon it starts, and
// it logs through them itself -- three processes writing one inherited
// descriptor, only one of which performs the rotation.
//
// Renaming the log strands the other two on the archive: d.log never
// grows again so it never rotates again, d.log.1 grows without bound,
// and the next rotation unlinks the inode they are still writing to.
// Copying the contents aside and truncating in place keeps one inode, so
// a holder that never hears about the rotation follows it anyway. This
// test proves that across a real process boundary: the parent opens the
// log, a child process rotates and dumps through the descriptor it
// inherited, and the parent's later writes have to land in the emptied
// d.log rather than in the archive.
func TestDiagnosticsRotationFollowsAProcessHoldingAnInheritedLog(t *testing.T) {
	home := t.TempDir()
	path := seedOversizedLog(t, home)

	holder, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = holder.Close() }()

	child := exec.Command(os.Args[0], "-test.run=^TestDiagnosticsRotationChildArm$")
	child.Env = append(os.Environ(), rotationChildEnv+"="+home)
	child.Stdout = holder
	child.Stderr = holder
	if err := child.Run(); err != nil {
		t.Fatalf("child arm: %v", err)
	}

	if _, err := holder.WriteString(parentMarker + "\n"); err != nil {
		t.Fatalf("holder write after rotation: %v", err)
	}

	live, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read d.log: %v", err)
	}
	archived, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("read d.log.1: %v", err)
	}
	if !strings.Contains(string(live), parentMarker) {
		t.Fatalf("the holder's post-rotation write did not land in d.log (%d bytes); "+
			"it followed the rotated-away file instead", len(live))
	}
	if strings.Contains(string(archived), parentMarker) {
		t.Fatal("the holder's post-rotation write landed in the archive d.log.1")
	}
	if !strings.Contains(string(live), "goroutine dump") {
		t.Fatalf("the rotating process's own dump did not land in d.log:\n%s", truncate(string(live), 400))
	}
	if int64(len(live)) > LogCapBytes {
		t.Fatalf("d.log is %d bytes, over the %d cap", len(live), LogCapBytes)
	}
	if int64(len(archived)) <= LogCapBytes {
		t.Fatalf("d.log.1 is %d bytes; it should hold the oversized log", len(archived))
	}
}

// TestDiagnosticsRotationChildArm is the second process of
// TestDiagnosticsRotationFollowsAProcessHoldingAnInheritedLog. It runs
// only when that test re-execs this binary; a normal run returns without
// doing anything.
func TestDiagnosticsRotationChildArm(t *testing.T) {
	home := os.Getenv(rotationChildEnv)
	if home == "" {
		return
	}
	d := &Daemon{cfg: Config{
		Home:    home,
		Version: "v0.0.0-test",
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}}
	d.writeDiagnosticDump()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func TestDiagnosticSummaryReportsWhatTheDaemonHolds(t *testing.T) {
	d := &Daemon{
		cfg:      Config{Version: "v1.2.3"},
		conns:    map[*conn]struct{}{{role: roleHolder}: {}, {role: roleWaiter}: {}},
		leaseRun: map[admission.LeaseID]string{"lease-1": "run-1"},
	}
	summary := d.diagnosticSummary()
	for _, want := range []string{"goroutines=", "conns=2", "holders=1", "waiters=1", "leases=1", "guards=", "v1.2.3"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary %q is missing %q", summary, want)
		}
	}
}

// TestDiagnosticSummaryDoesNotWaitOnTheDaemonMutex keeps the dump usable
// for the case it exists for: a daemon wedged holding its own lock must
// still answer, and say so.
func TestDiagnosticSummaryDoesNotWaitOnTheDaemonMutex(t *testing.T) {
	d := &Daemon{cfg: Config{Version: "v1.2.3"}}
	d.mu.Lock()
	defer d.mu.Unlock()
	summary := d.diagnosticSummary()
	if !strings.Contains(summary, "counts=unavailable") {
		t.Fatalf("summary under a held mutex = %q, want it to report the contention", summary)
	}
}
