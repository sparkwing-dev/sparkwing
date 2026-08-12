//go:build !windows

package wingd

import (
	"fmt"
	"os"
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

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := strings.Join(lines, "\n")
		mu.Unlock()
		if strings.Contains(got, "goroutine dump") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("SIGUSR1 produced no goroutine dump in the daemon log")
}

// TestDiagnosticsRotatesAnOversizedLogBeforeDumping pins the bound on a
// daemon that never restarts. Each dump appends up to 2MB and rotation
// used to happen only at spawn, so a resident daemon asked for a handful
// of dumps grew d.log without limit.
func TestDiagnosticsRotatesAnOversizedLogBeforeDumping(t *testing.T) {
	home := t.TempDir()
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
	if err := os.WriteFile(path, make([]byte, LogCapBytes+1), 0o600); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}

	// Stand in for a spawned daemon, whose output the client points at
	// d.log before the process starts.
	sink, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = sink.Close() }()
	prev := daemonLogSinks
	daemonLogSinks = func() []*os.File { return []*os.File{sink} }
	defer func() { daemonLogSinks = prev }()

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
	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("the oversized log was not rotated to d.log.1: %v", err)
	}
	if rotated.Size() <= LogCapBytes {
		t.Fatalf("d.log.1 is %d bytes; it should hold the oversized log", rotated.Size())
	}

	dumped, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read d.log: %v", err)
	}
	if got := strings.Count(string(dumped), "goroutine dump"); got != 2 {
		t.Fatalf("d.log holds %d dumps, want both of them:\n%s", got, truncate(string(dumped), 400))
	}
}

// TestDiagnosticsLeavesATerminalLogAlone keeps the rotation off a daemon
// running in the foreground, whose output is a console rather than the
// log file: renaming a log it is not writing, and redirecting the
// operator's terminal into a file, would both be wrong.
func TestDiagnosticsLeavesATerminalLogAlone(t *testing.T) {
	home := t.TempDir()
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
	if err := os.WriteFile(path, make([]byte, LogCapBytes+1), 0o600); err != nil {
		t.Fatalf("seed oversized log: %v", err)
	}

	console, err := os.CreateTemp(t.TempDir(), "console")
	if err != nil {
		t.Fatalf("stand-in console: %v", err)
	}
	defer func() { _ = console.Close() }()
	prev := daemonLogSinks
	daemonLogSinks = func() []*os.File { return []*os.File{console} }
	defer func() { daemonLogSinks = prev }()

	d := &Daemon{cfg: Config{Home: home, Version: "v0.0.0-test"}}
	d.writeDiagnosticDump()

	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("a log the daemon is not writing was rotated anyway")
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() <= LogCapBytes {
		t.Fatalf("d.log was disturbed (err=%v)", err)
	}
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
