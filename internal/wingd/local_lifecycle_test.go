package wingd

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/admission"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/localhostwire"
)

func TestLocalSubject(t *testing.T) {
	uid, foreign := uint32(1000), uint32(1001)
	for _, goos := range []string{"linux", "darwin"} {
		owner := localhostwire.LocalPeerIdentity{GOOS: goos, UID: &uid}
		if got, err := LocalSubject(owner, owner); err != nil || got != "local:1000" {
			t.Fatalf("%s: %q, %v", goos, got, err)
		}
		for _, peer := range []localhostwire.LocalPeerIdentity{{GOOS: goos}, {GOOS: goos, UID: &foreign}, {GOOS: "windows", UID: &uid}} {
			if _, err := LocalSubject(peer, owner); err == nil {
				t.Fatalf("accepted unverified peer %+v", peer)
			}
		}
	}
	owner := localhostwire.LocalPeerIdentity{GOOS: "windows", SID: "S-1-5-21-1000"}
	if got, err := LocalSubject(owner, owner); err != nil || got != "local:S-1-5-21-1000" {
		t.Fatalf("Windows: %q, %v", got, err)
	}
	for _, peer := range []localhostwire.LocalPeerIdentity{{GOOS: "windows"}, {GOOS: "windows", SID: "S-1-5-21-1001"}, {GOOS: "plan9"}} {
		if _, err := LocalSubject(peer, owner); err == nil {
			t.Fatalf("accepted unverified peer %+v", peer)
		}
	}
}

func TestLocalDaemonActivityAndProbeIdle(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	ledger, err := admission.New(admission.Config{})
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{cfg: Config{Now: func() time.Time { return now }}, ledger: ledger, lastActivity: now, conns: map[*conn]struct{}{{}: {}}}
	for _, hold := range []LocalActivity{{Runs: 1}, {QueuedTriggers: 1}, {HeldLeases: 1}, {DashboardConnections: 1}} {
		if err := d.setLocalActivity(hold); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * limits.LocalControllerIdleTimeout)
		if d.idleElapsed() != 0 {
			t.Fatalf("daemon idled with %+v", hold)
		}
		if err := d.setLocalActivity(LocalActivity{}); err != nil {
			t.Fatal(err)
		}
	}
	d.setStay(true)
	now = now.Add(limits.LocalControllerIdleTimeout)
	if d.idleElapsed() != 0 {
		t.Fatal("stay did not pin controller")
	}
	d.setStay(false)
	now = now.Add(limits.LocalControllerIdleTimeout)
	for range 10 {
		// safety: status reads and repeated zero-activity samples must not extend idle time.
		if err := d.setLocalActivity(LocalActivity{}); err != nil {
			t.Fatal(err)
		}
		if d.idleElapsed() != limits.LocalControllerIdleTimeout {
			t.Fatal("health observation extended idle deadline")
		}
	}
	if err := d.setLocalActivity(LocalActivity{Runs: -1}); err == nil {
		t.Fatal("negative count was accepted")
	}
	now = now.Add(-time.Nanosecond)
	if d.beginIdleShutdown(limits.LocalControllerIdleTimeout) {
		t.Fatal("controller exited before its idle boundary")
	}
	now = now.Add(time.Nanosecond)
	if !d.beginIdleShutdown(limits.LocalControllerIdleTimeout) {
		t.Fatal("controller did not exit at its idle boundary")
	}
}

type lifecycleRuntime struct {
	mu         sync.Mutex
	status     LocalStatus
	ready      chan struct{}
	stop       chan struct{}
	started    chan struct{}
	allow      chan struct{}
	runs       int
	drains     int
	failure    error
	runContext context.Context
}

func newLifecycleRuntime() *lifecycleRuntime {
	return &lifecycleRuntime{ready: make(chan struct{}), stop: make(chan struct{}), started: make(chan struct{}), allow: make(chan struct{})}
}

func (r *lifecycleRuntime) Run(ctx context.Context, opts LocalStartOptions) error {
	r.mu.Lock()
	r.runs++
	r.runContext = ctx
	r.mu.Unlock()
	close(r.started)
	if r.failure != nil {
		return r.failure
	}
	select {
	case <-r.allow:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	r.status = LocalStatus{Running: true, Stay: opts.Stay, Version: opts.Version, PID: 17}
	r.mu.Unlock()
	close(r.ready)
	select {
	case <-r.stop:
	case <-ctx.Done():
	}
	r.mu.Lock()
	r.status.Running = false
	r.mu.Unlock()
	return nil
}

func (r *lifecycleRuntime) AwaitReady(ctx context.Context) (LocalStatus, error) {
	select {
	case <-r.ready:
		return r.Status(ctx)
	case <-ctx.Done():
		return LocalStatus{}, ctx.Err()
	}
}

func (r *lifecycleRuntime) Status(context.Context) (LocalStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, nil
}

func (r *lifecycleRuntime) SetStay(_ context.Context, stay bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.status.Stay = stay
	return nil
}

func (r *lifecycleRuntime) EnsureDashboard(context.Context, string, bool) error { return nil }

func (r *lifecycleRuntime) Drain(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drains++
	close(r.stop)
	return nil
}

func TestLocalLifecycleConcurrentStartAndDrain(t *testing.T) {
	runtime := newLifecycleRuntime()
	lifecycle := NewLocalLifecycle(runtime, nil)
	ctx := context.Background()
	opts := LocalStartOptions{Home: t.TempDir(), Version: "v1"}
	results := make(chan error, 16)
	for range 16 {
		go func() {
			status, err := lifecycle.EnsureStarted(ctx, opts)
			if err == nil && (!status.Running || status.PID != 17) {
				err = errors.New("startup returned before readiness")
			}
			results <- err
		}()
	}
	<-runtime.started
	select {
	case <-results:
		t.Fatal("start returned before controller readiness")
	default:
	}
	close(runtime.allow)
	for range 16 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	opts.Stay = true
	status, err := lifecycle.StartDashboard(ctx, opts)
	if err != nil || !status.Stay {
		t.Fatalf("stay promotion: %+v, %v", status, err)
	}
	opts.Standalone = true
	if _, err := lifecycle.EnsureStarted(ctx, opts); err == nil {
		t.Fatal("standalone accepted a store with an existing controller")
	}
	if err := lifecycle.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if runtime.runs != 1 || runtime.drains != 1 {
		t.Fatalf("runs=%d drains=%d", runtime.runs, runtime.drains)
	}
}

func TestLocalLifecycleReportsStartupFailure(t *testing.T) {
	runtime := newLifecycleRuntime()
	runtime.failure = errors.New("store cannot open")
	lifecycle := NewLocalLifecycle(runtime, nil)
	if _, err := lifecycle.EnsureStarted(context.Background(), LocalStartOptions{Home: t.TempDir()}); !errors.Is(err, runtime.failure) {
		t.Fatalf("lost startup failure: %v", err)
	}
}

type lifecycleUnits struct{ units []ManagedUnit }

func (lifecycleUnits) InstallController(context.Context, LocalStartOptions) error { return nil }
func (lifecycleUnits) UninstallController(context.Context) error                  { return nil }
func (lifecycleUnits) Refresh(context.Context, string, string) error              { return nil }
func (u lifecycleUnits) Inspect(context.Context) ([]ManagedUnit, error)           { return u.units, nil }

func TestLocalDoctorReportsVersionAndRemovedUnitVerbs(t *testing.T) {
	runtime := newLifecycleRuntime()
	runtime.status = LocalStatus{Running: true, Version: "v1"}
	units := lifecycleUnits{[]ManagedUnit{
		{Kind: ManagedController, Name: "dashboard", Version: "v1", Arguments: []string{"serve", "start"}},
		{Kind: ManagedSchedule, Name: "timer", Version: "v2", Home: t.TempDir()},
	}}
	units.units[1].Arguments = []string{"__tick", "--home", units.units[1].Home}
	lifecycle := NewLocalLifecycle(runtime, units)
	findings, err := lifecycle.Doctor(context.Background(), "v2")
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, finding := range findings {
		codes = append(codes, finding.Code)
		if finding.Hint == "" {
			t.Fatal("finding has no repair command")
		}
	}
	if !slices.Equal(codes, []string{"version_skew", "unit_version_skew", "removed_unit_verb"}) {
		t.Fatalf("findings: %+v", findings)
	}
}

func TestLocalLifecycleContextOwnership(t *testing.T) {
	for _, standalone := range []bool{false, true} {
		runtime := newLifecycleRuntime()
		close(runtime.allow)
		lifecycle := NewLocalLifecycle(runtime, nil)
		ctx, cancel := context.WithCancel(context.Background())
		_, err := lifecycle.EnsureStarted(ctx, LocalStartOptions{Home: t.TempDir(), Standalone: standalone})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		runtime.mu.Lock()
		cancelled := runtime.runContext.Err() != nil
		runtime.mu.Unlock()
		if cancelled != standalone {
			t.Fatalf("standalone=%t runtime cancelled=%t", standalone, cancelled)
		}
		if err := lifecycle.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

type joiningRuntime struct {
	*lifecycleRuntime
	waitStarted, waitCancelled, waitExited chan struct{}
	runCancelled, runExited                chan struct{}
	releaseWait, releaseRun                chan struct{}
	startupFailure                         error
}

func (r *joiningRuntime) Run(ctx context.Context, _ LocalStartOptions) error {
	defer close(r.runExited)
	close(r.started)
	if r.startupFailure != nil {
		return r.startupFailure
	}
	<-ctx.Done()
	close(r.runCancelled)
	<-r.releaseRun
	return ctx.Err()
}

func (r *joiningRuntime) AwaitReady(ctx context.Context) (LocalStatus, error) {
	defer close(r.waitExited)
	close(r.waitStarted)
	<-ctx.Done()
	close(r.waitCancelled)
	<-r.releaseWait
	return LocalStatus{}, ctx.Err()
}

func TestLocalLifecycleJoinsFailedStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, failRun := range []bool{false, true} {
			r := &joiningRuntime{
				lifecycleRuntime: newLifecycleRuntime(),
				waitStarted:      make(chan struct{}), waitCancelled: make(chan struct{}), waitExited: make(chan struct{}),
				runCancelled: make(chan struct{}), runExited: make(chan struct{}),
				releaseWait: make(chan struct{}), releaseRun: make(chan struct{}),
			}
			if failRun {
				r.startupFailure = errors.New("store failed before readiness")
			}
			ctx, cancel := context.WithCancel(context.Background())
			lifecycle := NewLocalLifecycle(r, nil)
			home := t.TempDir()
			result := make(chan error, 1)
			go func() {
				_, err := lifecycle.EnsureStarted(ctx, LocalStartOptions{Home: home})
				result <- err
			}()
			<-r.started
			<-r.waitStarted
			cancel()
			<-r.waitCancelled
			returned := false
			checkWaiting := func(stage string) {
				synctest.Wait()
				select {
				case <-result:
					returned = true
					t.Errorf("startup returned before %s cleanup finished", stage)
				default:
				}
			}
			checkWaiting("readiness")
			close(r.releaseWait)
			<-r.waitExited
			if !failRun {
				<-r.runCancelled
				checkWaiting("runtime")
			}
			close(r.releaseRun)
			<-r.runExited
			if !returned {
				if err := <-result; err == nil {
					t.Error("failed startup returned success")
				}
			}
		}
	})
}

func (d *Daemon) idleElapsed() time.Duration {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.idleElapsedLocked()
}
