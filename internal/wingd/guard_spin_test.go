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

func runGuardLoopFor(t *testing.T, d *Daemon, window time.Duration) {
	t.Helper()
	stop := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		d.guardLoop(stop)
	}()

	observation := time.NewTimer(window)
	defer observation.Stop()
	<-observation.C
	close(stop)

	join := time.NewTimer(time.Second)
	defer join.Stop()
	select {
	case <-finished:
	case <-join.C:
		t.Fatal("guard loop did not stop within one second")
	}
}

func TestGuardLoopBacksOffWhileInspectionFails(t *testing.T) {
	const window = 500 * time.Millisecond
	const interval = 10 * time.Millisecond
	inspector := &countingGuardInspector{err: errors.New("process table unavailable")}
	d := guardSweepDaemon(inspector, 4, interval)

	runGuardLoopFor(t, d, window)

	snapshots, _ := inspector.counts()
	unpaced := int(window / interval)
	if snapshots >= unpaced/2 {
		t.Fatalf("failing sweeps = %d in %s; unpaced retry would be about %d, so the backoff is not holding", snapshots, window, unpaced)
	}
	if snapshots < 2 {
		t.Fatalf("failing sweeps = %d; the loop stopped retrying entirely", snapshots)
	}
}

func TestGuardLoopKeepsFullCadenceWhileInspectionWorks(t *testing.T) {
	const window = 300 * time.Millisecond
	const interval = 10 * time.Millisecond
	inspector := &countingGuardInspector{}
	d := guardSweepDaemon(inspector, 4, interval)

	runGuardLoopFor(t, d, window)

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

type partiallyFailingInspector struct {
	mu        sync.Mutex
	snapshots int
	failPID   int
}

func (g *partiallyFailingInspector) Validate(wingwire.ProcessSession) error { return nil }

func (g *partiallyFailingInspector) Quiescent(wingwire.ProcessSession) (bool, error) {
	return false, nil
}

func (g *partiallyFailingInspector) Empty(session wingwire.ProcessSession) (bool, error) {
	if session.LeaderPID == g.failPID {
		return false, errors.New("inspection failed for this session")
	}
	return false, nil
}

func (g *partiallyFailingInspector) Terminate(wingwire.ProcessSession) error { return nil }

func (g *partiallyFailingInspector) EmptySnapshot() (func(wingwire.ProcessSession) (bool, error), error) {
	g.mu.Lock()
	g.snapshots++
	g.mu.Unlock()
	return g.Empty, nil
}

func (g *partiallyFailingInspector) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshots
}

func TestOneBrokenGuardDoesNotSlowTheSweep(t *testing.T) {
	const window = 300 * time.Millisecond
	const interval = 10 * time.Millisecond
	inspector := &partiallyFailingInspector{failPID: 1000}
	d := guardSweepDaemon(inspector, 3, interval)

	if err := d.reconcileGuards(); err != nil {
		t.Fatalf("a sweep with one broken guard reported failure: %v", err)
	}

	runGuardLoopFor(t, d, window)

	if want := int(window/interval) / 3; inspector.count() < want {
		t.Fatalf("sweeps = %d in %s, want at least %d; one broken guard slowed the whole loop", inspector.count(), window, want)
	}
}

func TestEveryGuardFailingStillBacksOff(t *testing.T) {
	const window = 500 * time.Millisecond
	const interval = 10 * time.Millisecond
	inspector := &partiallyFailingInspector{failPID: 1000}
	d := guardSweepDaemon(inspector, 1, interval)

	if err := d.reconcileGuards(); err == nil {
		t.Fatal("a sweep whose only guard failed reported success")
	}

	runGuardLoopFor(t, d, window)

	unpaced := int(window / interval)
	if got := inspector.count(); got >= unpaced/2 {
		t.Fatalf("sweeps = %d in %s; unpaced would be about %d, so the backoff is not holding", got, window, unpaced)
	}
}
