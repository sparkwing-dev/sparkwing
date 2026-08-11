package wingd

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

// countingGuardInspector records how much kernel work a guard sweep asks
// for: how many process-table snapshots, and how many session answers off
// them. Every session it is asked about is reported live, so a sweep never
// completes a guard and the loop keeps sweeping.
type countingGuardInspector struct {
	mu        sync.Mutex
	snapshots int
	sessions  int
	err       error
}

func (g *countingGuardInspector) Validate(wingwire.ProcessSession) error { return nil }

func (g *countingGuardInspector) Quiescent(wingwire.ProcessSession) (bool, error) {
	return false, nil
}

func (g *countingGuardInspector) Empty(wingwire.ProcessSession) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sessions++
	return false, g.err
}

func (g *countingGuardInspector) Terminate(wingwire.ProcessSession) error { return nil }

func (g *countingGuardInspector) EmptySnapshot() (func(wingwire.ProcessSession) (bool, error), error) {
	g.mu.Lock()
	g.snapshots++
	err := g.err
	g.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return func(wingwire.ProcessSession) (bool, error) {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.sessions++
		return false, nil
	}, nil
}

func (g *countingGuardInspector) counts() (snapshots, sessions int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshots, g.sessions
}

func guardSweepDaemon(inspector SessionGuardInspector, guards int, interval time.Duration) *Daemon {
	d := &Daemon{
		cfg:            Config{GuardInterval: interval},
		guardInspector: inspector,
		guards:         map[admission.LeaseID]*sessionGuardState{},
		quit:           make(chan struct{}),
	}
	for i := range guards {
		id := admission.LeaseID(fmt.Sprintf("lease-%d", i))
		pid := 1000 + i
		d.guards[id] = &sessionGuardState{
			persistedGuard: persistedGuard{
				LeaseID: id,
				RunID:   fmt.Sprintf("run-%d", i),
				Session: wingwire.ProcessSession{
					LeaderPID: pid, SessionID: pid, BirthToken: fmt.Sprintf("birth-%d", pid),
				},
			},
			disconnected: true,
		}
	}
	return d
}

// TestReconcileGuardsTakesOneProcessSnapshotPerSweep pins the cost of
// watching guarded sessions. Asking the kernel per guard makes the sweep
// cost N process-table listings -- a `ps` fork plus a syscall per live
// process, ten times a second -- which is the daemon spin this bounds.
func TestReconcileGuardsTakesOneProcessSnapshotPerSweep(t *testing.T) {
	const guards, sweeps = 8, 5
	inspector := &countingGuardInspector{}
	d := guardSweepDaemon(inspector, guards, 10*time.Millisecond)

	for i := 0; i < sweeps; i++ {
		if err := d.reconcileGuards(); err != nil {
			t.Fatalf("sweep %d: %v", i, err)
		}
	}

	snapshots, sessions := inspector.counts()
	if snapshots != sweeps {
		t.Fatalf("process-table snapshots = %d, want one per sweep (%d)", snapshots, sweeps)
	}
	if sessions != sweeps*guards {
		t.Fatalf("session answers = %d, want %d (every guard judged on every sweep)", sessions, sweeps*guards)
	}
}

// TestReconcileGuardsWithoutSnapshotSupportStillJudgesEveryGuard keeps the
// batch seam optional: an inspector that only answers per session is asked
// per session.
func TestReconcileGuardsWithoutSnapshotSupportStillJudgesEveryGuard(t *testing.T) {
	inspector := &perSessionGuardInspector{}
	d := guardSweepDaemon(inspector, 3, 10*time.Millisecond)

	if err := d.reconcileGuards(); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if got := inspector.calls(); got != 3 {
		t.Fatalf("per-session inspections = %d, want 3", got)
	}
}

type perSessionGuardInspector struct {
	mu    sync.Mutex
	count int
}

func (g *perSessionGuardInspector) Validate(wingwire.ProcessSession) error { return nil }

func (g *perSessionGuardInspector) Quiescent(wingwire.ProcessSession) (bool, error) {
	return false, nil
}

func (g *perSessionGuardInspector) Empty(wingwire.ProcessSession) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.count++
	return false, nil
}

func (g *perSessionGuardInspector) Terminate(wingwire.ProcessSession) error { return nil }

func (g *perSessionGuardInspector) calls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.count
}

// TestGuardLoopBacksOffWhileInspectionFails is the regression the reported
// spin asks for: a guard state the daemon cannot inspect -- a wedged or
// unreadable process table -- must cost a probe every few seconds, not a
// probe every interval for as long as the daemon lives.
func TestGuardLoopBacksOffWhileInspectionFails(t *testing.T) {
	const window = 500 * time.Millisecond
	const interval = 10 * time.Millisecond
	inspector := &countingGuardInspector{err: errors.New("process table unavailable")}
	d := guardSweepDaemon(inspector, 4, interval)

	done := make(chan struct{})
	go d.guardLoop(done)
	time.Sleep(window)
	close(done)

	snapshots, _ := inspector.counts()
	unpaced := int(window / interval)
	if snapshots >= unpaced/2 {
		t.Fatalf("failing sweeps = %d in %s; unpaced retry would be about %d, so the backoff is not holding", snapshots, window, unpaced)
	}
	if snapshots < 2 {
		t.Fatalf("failing sweeps = %d; the loop stopped retrying entirely", snapshots)
	}
}

// TestGuardLoopKeepsFullCadenceWhileInspectionWorks is the other half: the
// backoff must not slow a healthy daemon, which detects an orphaned
// session within one interval.
func TestGuardLoopKeepsFullCadenceWhileInspectionWorks(t *testing.T) {
	const window = 300 * time.Millisecond
	const interval = 10 * time.Millisecond
	inspector := &countingGuardInspector{}
	d := guardSweepDaemon(inspector, 4, interval)

	done := make(chan struct{})
	go d.guardLoop(done)
	time.Sleep(window)
	close(done)

	snapshots, _ := inspector.counts()
	if want := int(window/interval) / 3; snapshots < want {
		t.Fatalf("healthy sweeps = %d in %s, want at least %d; the sweep cadence regressed", snapshots, window, want)
	}
}

func TestNextGuardDelayDoublesUpToTheCap(t *testing.T) {
	base := 100 * time.Millisecond
	if got := nextGuardDelay(base, base); got != 200*time.Millisecond {
		t.Fatalf("first backoff = %s, want 200ms", got)
	}
	if got := nextGuardDelay(4*time.Second, base); got != maxGuardInterval {
		t.Fatalf("backoff past the cap = %s, want %s", got, maxGuardInterval)
	}
	if got := nextGuardDelay(maxGuardInterval, base); got != maxGuardInterval {
		t.Fatalf("backoff at the cap = %s, want %s", got, maxGuardInterval)
	}
	slow := 10 * time.Second
	if got := nextGuardDelay(slow, slow); got != slow {
		t.Fatalf("backoff below a configured interval = %s, want %s", got, slow)
	}
}
