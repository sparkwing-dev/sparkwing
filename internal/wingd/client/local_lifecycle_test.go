package client

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type lifecycleOwner struct {
	status             wingd.LocalStatus
	err                error
	dashboards, drains int
	address            string
	readOnly           bool
}

func (o *lifecycleOwner) Status(ctx context.Context) (wingd.LocalStatus, error) {
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	return o.status, o.err
}

func (o *lifecycleOwner) EnsureDashboard(_ context.Context, address string, readOnly bool) error {
	o.dashboards++
	o.address, o.readOnly = address, readOnly
	return o.err
}

func (o *lifecycleOwner) Drain(context.Context) error {
	o.drains++
	o.status.Running = false
	return o.err
}

type localRuntimeFixture struct {
	lifecycleOwner
	mu      sync.Mutex
	starts  int
	failure error
	onRun   func()
}

func (r *localRuntimeFixture) Run(ctx context.Context, _ wingd.LocalStartOptions) error {
	r.mu.Lock()
	r.starts++
	r.mu.Unlock()
	if r.onRun != nil {
		r.onRun()
	}
	if r.failure != nil {
		return r.failure
	}
	<-ctx.Done()
	return ctx.Err()
}

func (r *localRuntimeFixture) AwaitReady(ctx context.Context) (wingd.LocalStatus, error) {
	<-ctx.Done()
	return wingd.LocalStatus{}, ctx.Err()
}

func localLifecycleFixture(t *testing.T, home string, runtime wingd.LocalRuntime, owner controllerOwner) *processLifecycle {
	t.Helper()
	return newProcessLifecycle(home, runtime, nil, owner, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
		t.Fatal("unexpected detached startup")
		return wingd.LocalStatus{}, nil
	})
}

func TestLocalLifecycleReusesForeignOwner(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := localLifecycleFixture(t, home, local, owner)
	opts := wingd.LocalStartOptions{Home: home, Version: "v1", DashboardAddress: "127.0.0.1:9173", DashboardReadOnly: true}
	status, err := lifecycle.StartDashboard(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if status.PID != 72 || status.Version != "v1" || local.starts != 0 || owner.dashboards != 1 || owner.address != opts.DashboardAddress || !owner.readOnly {
		t.Fatalf("owner=%+v local starts=%d dashboards=%d", status, local.starts, owner.dashboards)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if owner.drains != 1 || local.drains != 0 {
		t.Fatalf("drains: owner=%d local=%d", owner.drains, local.drains)
	}
}

func TestLocalLifecycleReusesWinnerAfterLosingElection(t *testing.T) {
	home := t.TempDir()
	owner := &lifecycleOwner{err: ErrNoDaemon}
	local := &localRuntimeFixture{failure: wingd.ErrNotElected, onRun: func() {
		owner.err = nil
		owner.status = wingd.LocalStatus{Running: true, PID: 84, Version: "v1"}
	}}
	lifecycle := localLifecycleFixture(t, home, local, owner)
	status, err := lifecycle.LocalLifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{Home: home})
	if err != nil {
		t.Fatal(err)
	}
	if status.PID != 84 || local.starts != 1 {
		t.Fatalf("owner=%+v local starts=%d", status, local.starts)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalLifecycleRefusesStandaloneAndForeignHome(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := localLifecycleFixture(t, home, local, owner)
	for _, opts := range []wingd.LocalStartOptions{
		{Home: home, Standalone: true},
		{Home: t.TempDir(), DashboardAddress: "127.0.0.1:9173"},
	} {
		if _, err := lifecycle.StartDashboard(t.Context(), opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
	if local.starts != 0 || owner.dashboards != 0 || owner.drains != 0 {
		t.Fatalf("refused operation changed an owner: local=%+v owner=%+v", local, owner)
	}
}

func TestLocalLifecyclePreservesOwnerAndCancellationFailures(t *testing.T) {
	for _, failure := range []error{errDaemonUnreachable, errDaemonWedged, errors.New("unsupported local control"), context.Canceled} {
		home := t.TempDir()
		local := &localRuntimeFixture{}
		owner := &lifecycleOwner{err: failure}
		lifecycle := localLifecycleFixture(t, home, local, owner)
		if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, failure) {
			t.Fatalf("owner error=%v, want %v", err, failure)
		}
		if local.starts != 0 {
			t.Fatal("started a second owner after a failed query")
		}
	}
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := localLifecycleFixture(t, t.TempDir(), local, owner)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := lifecycle.EnsureStarted(ctx, wingd.LocalStartOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if local.starts != 0 {
		t.Fatal("cancelled operation changed an owner")
	}
}

func TestLocalLifecyclePreservesElectionCleanupFailure(t *testing.T) {
	cleanup := errors.New("cleanup failed")
	owner := &lifecycleOwner{err: ErrNoDaemon}
	local := &localRuntimeFixture{failure: errors.Join(wingd.ErrNotElected, cleanup), onRun: func() {
		owner.err = nil
		owner.status = wingd.LocalStatus{Running: true, PID: 84, Version: "v1"}
	}}
	lifecycle := localLifecycleFixture(t, t.TempDir(), local, owner)
	if _, err := lifecycle.LocalLifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, cleanup) {
		t.Fatalf("election cleanup error lost: %v", err)
	}
}

func TestLocalLifecycleRetainsLocalOwnerAndStartupFailure(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{lifecycleOwner: lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 17, Version: "v1"}}}
	owner := &lifecycleOwner{err: errors.New("foreign transport must remain unused")}
	lifecycle := localLifecycleFixture(t, home, local, owner)
	status, err := lifecycle.StartDashboard(t.Context(), wingd.LocalStartOptions{DashboardAddress: "127.0.0.1:9173"})
	if err != nil || status.PID != 17 {
		t.Fatalf("local owner=%+v err=%v", status, err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if local.dashboards != 1 || local.drains != 1 || owner.dashboards != 0 || owner.drains != 0 {
		t.Fatalf("wrong owner changed: local=%+v foreign=%+v", local, owner)
	}
	startup := errors.New("controller factory failed")
	local = &localRuntimeFixture{failure: startup}
	lifecycle = localLifecycleFixture(t, home, local, &lifecycleOwner{err: ErrNoDaemon})
	if _, err := lifecycle.LocalLifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, startup) || local.starts != 1 {
		t.Fatalf("absent owner startup=%v starts=%d", err, local.starts)
	}
}

func TestLocalLifecycleConcurrentForeignOwnerReuse(t *testing.T) {
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := localLifecycleFixture(t, t.TempDir(), local, owner)
	var callers sync.WaitGroup
	for range 20 {
		callers.Go(func() {
			status, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{})
			if err != nil || status.PID != 72 {
				t.Errorf("owner=%+v err=%v", status, err)
			}
		})
	}
	callers.Wait()
	if local.starts != 0 {
		t.Fatalf("starts=%d", local.starts)
	}
}

func TestLocalLifecycleConstructorRejectsMissingRuntime(t *testing.T) {
	if _, err := NewLocalLifecycle(t.TempDir(), nil, nil); err == nil {
		t.Fatal("accepted a missing runtime")
	}
}
