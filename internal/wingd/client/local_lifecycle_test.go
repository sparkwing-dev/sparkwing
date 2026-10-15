package client

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type lifecycleOwner struct {
	status                    wingd.LocalStatus
	err                       error
	stays, dashboards, drains int
	address                   string
	onStay                    func()
}

func (o *lifecycleOwner) Status(ctx context.Context) (wingd.LocalStatus, error) {
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	return o.status, o.err
}

func (o *lifecycleOwner) SetStay(_ context.Context, stay bool) error {
	o.stays++
	o.status.Stay = stay
	if o.onStay != nil {
		o.onStay()
	}
	return o.err
}

func (o *lifecycleOwner) EnsureDashboard(_ context.Context, address string) error {
	o.dashboards++
	o.address = address
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

func TestLocalLifecycleReusesForeignOwner(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := newOwnerLifecycle(home, local, nil, owner)
	opts := wingd.LocalStartOptions{Home: home, Version: "v2", Stay: true, DashboardAddress: "127.0.0.1:9173"}
	status, err := lifecycle.StartDashboard(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if status.PID != 72 || status.Version != "v1" || !status.Stay || local.starts != 0 || owner.stays != 1 || owner.dashboards != 1 || owner.address != opts.DashboardAddress {
		t.Fatalf("owner=%+v local starts=%d stays=%d dashboards=%d", status, local.starts, owner.stays, owner.dashboards)
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
	lifecycle := newOwnerLifecycle(home, local, nil, owner)
	status, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{Home: home, Stay: true})
	if err != nil {
		t.Fatal(err)
	}
	if status.PID != 84 || !status.Stay || local.starts != 1 || owner.stays != 1 {
		t.Fatalf("owner=%+v local starts=%d stays=%d", status, local.starts, owner.stays)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLocalLifecycleRefusesStandaloneAndForeignHome(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := newOwnerLifecycle(home, local, nil, owner)
	for _, opts := range []wingd.LocalStartOptions{
		{Home: home, Standalone: true, Stay: true},
		{Home: t.TempDir(), Stay: true, DashboardAddress: "127.0.0.1:9173"},
	} {
		if _, err := lifecycle.StartDashboard(t.Context(), opts); err == nil {
			t.Fatalf("accepted %+v", opts)
		}
	}
	if local.starts != 0 || owner.stays != 0 || owner.dashboards != 0 || owner.drains != 0 {
		t.Fatalf("refused operation changed an owner: local=%+v owner=%+v", local, owner)
	}
}

func TestLocalLifecyclePreservesOwnerAndCancellationFailures(t *testing.T) {
	for _, failure := range []error{ErrDaemonUnreachable, ErrDaemonWedged, errors.New("unsupported local control"), context.Canceled} {
		home := t.TempDir()
		local := &localRuntimeFixture{}
		owner := &lifecycleOwner{err: failure}
		lifecycle := newOwnerLifecycle(home, local, nil, owner)
		if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, failure) {
			t.Fatalf("owner error=%v, want %v", err, failure)
		}
		if local.starts != 0 {
			t.Fatal("started a second owner after a failed query")
		}
	}
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := newOwnerLifecycle(t.TempDir(), local, nil, owner)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := lifecycle.EnsureStarted(ctx, wingd.LocalStartOptions{Stay: true}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if owner.stays != 0 || local.starts != 0 {
		t.Fatal("cancelled operation changed an owner")
	}
}

func TestLocalLifecycleRequiresOwnerAfterPromotion(t *testing.T) {
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	owner.onStay = func() { owner.status.Running = false }
	lifecycle := newOwnerLifecycle(t.TempDir(), &localRuntimeFixture{}, nil, owner)
	if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{Stay: true}); err == nil {
		t.Fatal("reported successful startup after the owner stopped")
	}
}

func TestLocalLifecyclePreservesElectionCleanupFailure(t *testing.T) {
	cleanup := errors.New("cleanup failed")
	owner := &lifecycleOwner{err: ErrNoDaemon}
	local := &localRuntimeFixture{failure: errors.Join(wingd.ErrNotElected, cleanup), onRun: func() {
		owner.err = nil
		owner.status = wingd.LocalStatus{Running: true, PID: 84, Version: "v1"}
	}}
	lifecycle := newOwnerLifecycle(t.TempDir(), local, nil, owner)
	if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, cleanup) {
		t.Fatalf("election cleanup error lost: %v", err)
	}
}

func TestLocalLifecycleRetainsLocalOwnerAndStartupFailure(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{lifecycleOwner: lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 17, Version: "v1"}}}
	owner := &lifecycleOwner{err: errors.New("foreign transport must remain unused")}
	lifecycle := newOwnerLifecycle(home, local, nil, owner)
	status, err := lifecycle.StartDashboard(t.Context(), wingd.LocalStartOptions{Stay: true, DashboardAddress: "127.0.0.1:9173"})
	if err != nil || status.PID != 17 || !status.Stay {
		t.Fatalf("local owner=%+v err=%v", status, err)
	}
	if err := lifecycle.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if local.stays != 1 || local.dashboards != 1 || local.drains != 1 || owner.stays != 0 || owner.dashboards != 0 || owner.drains != 0 {
		t.Fatalf("wrong owner changed: local=%+v foreign=%+v", local, owner)
	}
	startup := errors.New("controller factory failed")
	local = &localRuntimeFixture{failure: startup}
	lifecycle = newOwnerLifecycle(home, local, nil, &lifecycleOwner{err: ErrNoDaemon})
	if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, startup) || local.starts != 1 {
		t.Fatalf("absent owner startup=%v starts=%d", err, local.starts)
	}
}

func TestLocalLifecycleConcurrentForeignOwnerReuse(t *testing.T) {
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{status: wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}}
	lifecycle := newOwnerLifecycle(t.TempDir(), local, nil, owner)
	var callers sync.WaitGroup
	for range 20 {
		callers.Go(func() {
			status, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{Stay: true})
			if err != nil || status.PID != 72 || !status.Stay {
				t.Errorf("owner=%+v err=%v", status, err)
			}
		})
	}
	callers.Wait()
	if local.starts != 0 || owner.stays != 1 {
		t.Fatalf("starts=%d stay promotions=%d", local.starts, owner.stays)
	}
}

func TestLocalLifecycleConstructorRejectsMissingRuntime(t *testing.T) {
	if _, err := NewLocalLifecycle(t.TempDir(), nil, nil); err == nil {
		t.Fatal("accepted a missing runtime")
	}
}
