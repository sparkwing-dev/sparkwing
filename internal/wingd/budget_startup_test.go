package wingd_test

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// logCapture is a thread-safe sink for the daemon's operational log lines,
// which the sampler goroutine writes concurrently with startup.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func (l *logCapture) contains(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ln := range l.lines {
		if strings.Contains(ln, sub) {
			return true
		}
	}
	return false
}

func (l *logCapture) joined() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func TestDaemonStartup_LogsBudgetClampToMachine(t *testing.T) {
	log := &logCapture{}
	budget, err := wingd.ParseBudget("100")
	if err != nil {
		t.Fatalf("parse budget: %v", err)
	}
	td := startDaemon(t, wingd.Config{
		Home:    shortHome(t),
		Sampler: newFakeSampler(4, 8<<30),
		Budget:  budget,
		Logf:    log.logf,
	})
	waitForLog(t, log, "clamped to machine")
	_ = td

	if !log.contains("requested 100.0 cores exceeds machine 4.0") {
		t.Errorf("clamp log missing the requested/machine numbers:\n%s", log.joined())
	}
}

func TestDaemonStartup_LogsIgnoreExternal(t *testing.T) {
	log := &logCapture{}
	budget, err := wingd.ParseBudget("ignore-external")
	if err != nil {
		t.Fatalf("parse budget: %v", err)
	}
	td := startDaemon(t, wingd.Config{
		Home:    shortHome(t),
		Sampler: newFakeSampler(8, 8<<30),
		Budget:  budget,
		Logf:    log.logf,
	})
	waitForLog(t, log, "ignoring external load in admission headroom")
	_ = td
}

func waitForLog(t *testing.T, log *logCapture, sub string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if log.contains(sub) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("expected a log line containing %q, got:\n%s", sub, log.joined())
}
