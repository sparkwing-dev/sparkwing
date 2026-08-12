//go:build !windows

package wingd

import (
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
