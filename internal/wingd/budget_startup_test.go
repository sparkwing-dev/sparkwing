package wingd_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type logCapture struct {
	mu      sync.Mutex
	lines   []string
	changed chan struct{}
}

func (l *logCapture) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimSpace(fmt.Sprintf(format, args...)))
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
}

func (l *logCapture) updates() <-chan struct{} {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.changed == nil {
		l.changed = make(chan struct{})
	}
	return l.changed
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
	budget, err := userconfig.ParseBudget("100")
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
	budget, err := userconfig.ParseBudget("ignore-external")
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
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for {
		changed := log.updates()
		if log.contains(sub) {
			return
		}
		select {
		case <-changed:
		case <-ctx.Done():
			t.Fatalf("expected a log line containing %q, got:\n%s", sub, log.joined())
		}
	}
}
