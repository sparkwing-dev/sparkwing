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
	ledger, err := admission.New(admission.Config{TotalCores: 2, TotalMemoryBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var lines []string
	d := &Daemon{
		cfg: Config{Version: "v0.0.0-test", Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, format)
		}},
		ledger: ledger,
		quit:   make(chan struct{}),
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
	ledger, err := admission.New(admission.Config{TotalCores: 2, TotalMemoryBytes: 1 << 30})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Version: "v1.2.3"}, ledger: ledger}
	summary := d.diagnosticSummary()
	for _, want := range []string{"goroutines=", "conns=", "guards=", "waiters=", "v1.2.3"} {
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
