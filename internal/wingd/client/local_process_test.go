package client

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func TestLocalProcessLifecycleStartsDetachedAndReusesOwner(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{failure: errors.New("ordinary startup opened the store in the CLI")}
	owner := &lifecycleOwner{err: ErrNoDaemon}
	starts := 0
	lifecycle := newProcessLifecycle(home, local, nil, owner, func(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
		starts++
		if ctx.Err() != nil || opts.Home != home || opts.Version != "v2" || opts.Executable != "/sparkwing" || opts.Standalone {
			t.Fatalf("spawn options: %+v, context: %v", opts, ctx.Err())
		}
		owner.err = nil
		owner.status = wingd.LocalStatus{Running: true, PID: 72, Version: "v2"}
		return owner.status, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	opts := wingd.LocalStartOptions{Home: home, Executable: "/sparkwing", Version: "v2", Stay: true, DashboardAddress: "127.0.0.1:4343"}
	status, err := lifecycle.StartDashboard(ctx, opts)
	if err != nil || status.PID != 72 || !status.Stay || local.starts != 0 || starts != 1 || owner.dashboards != 1 {
		t.Fatalf("startup: %+v, %v, local=%d detached=%d dashboard=%d", status, err, local.starts, starts, owner.dashboards)
	}
	cancel()
	opts.Version = "v3"
	var callers sync.WaitGroup
	for range 20 {
		callers.Go(func() {
			status, err := lifecycle.EnsureStarted(t.Context(), opts)
			if err != nil || status.PID != 72 || !status.Stay || status.Version != "v2" {
				t.Errorf("reuse: %+v, %v", status, err)
			}
		})
	}
	callers.Wait()
	if starts != 1 || owner.stays != 1 || local.starts != 0 || owner.drains != 0 {
		t.Fatalf("request cancellation or reuse changed ownership: starts=%d owner=%+v", starts, owner)
	}
	if err := lifecycle.Stop(t.Context()); err != nil || owner.drains != 1 {
		t.Fatalf("stop: %v, drains=%d", err, owner.drains)
	}
}

func FuzzLocalProcessLifecycleKeepsOwnership(f *testing.F) {
	f.Add(false, true, false, uint8(0))
	f.Add(true, false, true, uint8(1))
	f.Add(false, true, true, uint8(2))
	f.Fuzz(func(t *testing.T, present, stay, dashboard bool, failure uint8) {
		home := t.TempDir()
		local := &localRuntimeFixture{failure: errors.New("CLI opened the store")}
		owner := &lifecycleOwner{err: ErrNoDaemon}
		if present {
			owner.err = nil
			owner.status = wingd.LocalStatus{Running: true, PID: 72, Version: "v1"}
		}
		spawnFailure := errors.New("spawn failed")
		starts := 0
		lifecycle := newProcessLifecycle(home, local, nil, owner, func(_ context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
			starts++
			if opts.Home != home || opts.Standalone {
				t.Fatal("spawn escaped its home or execution mode")
			}
			if failure%4 == 1 {
				return wingd.LocalStatus{}, spawnFailure
			}
			owner.err = nil
			owner.status = wingd.LocalStatus{Running: true, PID: 84, Version: "v2"}
			return owner.status, nil
		})
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		opts := wingd.LocalStartOptions{Home: home, Version: "v2", Stay: stay}
		if dashboard {
			opts.DashboardAddress = "127.0.0.1:4343"
		}
		if failure%4 == 2 {
			cancel()
		} else if failure%4 == 3 {
			opts.Home = t.TempDir()
		}
		status, err := lifecycle.StartDashboard(ctx, opts)
		refused := failure%4 >= 2 || (!present && failure%4 == 1)
		if (err != nil) != refused || (!refused && (!status.Running || (stay && !status.Stay))) {
			t.Fatalf("unexpected startup: status=%+v error=%v refused=%t", status, err, refused)
		}
		if local.starts != 0 || starts > 1 || owner.drains != 0 {
			t.Fatal("request changed process ownership")
		}
		if refused && (owner.stays != 0 || owner.dashboards != 0) {
			t.Fatal("refused request mutated a controller")
		}
		if present && starts != 0 {
			t.Fatal("ready owner was replaced")
		}
	})
}

func TestLocalProcessLifecycleKeepsStandaloneInProcess(t *testing.T) {
	startup := errors.New("shared local services failed")
	local := &localRuntimeFixture{failure: startup}
	owner := &lifecycleOwner{err: ErrNoDaemon}
	lifecycle := newProcessLifecycle(t.TempDir(), local, nil, owner, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
		t.Fatal("standalone started a detached controller")
		return wingd.LocalStatus{}, nil
	})
	_, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{Standalone: true})
	if !errors.Is(err, startup) || local.starts != 1 {
		t.Fatalf("standalone: %v, local starts=%d", err, local.starts)
	}
}

func TestLocalProcessLifecycleRefusesUnknownOwnerAndFailedStartup(t *testing.T) {
	for _, failure := range []error{ErrDaemonUnreachable, ErrDaemonWedged, context.Canceled} {
		t.Run(failure.Error(), func(t *testing.T) {
			local := &localRuntimeFixture{}
			owner := &lifecycleOwner{err: failure}
			lifecycle := newProcessLifecycle(t.TempDir(), local, nil, owner, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
				t.Fatal("started another controller without proving absence")
				return wingd.LocalStatus{}, nil
			})
			if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, failure) || local.starts != 0 {
				t.Fatalf("query: %v, local starts=%d", err, local.starts)
			}
		})
	}
	failure := errors.New("detached controller failed to open services")
	local := &localRuntimeFixture{}
	lifecycle := newProcessLifecycle(t.TempDir(), local, nil, &lifecycleOwner{err: ErrNoDaemon}, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
		return wingd.LocalStatus{}, failure
	})
	if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{}); !errors.Is(err, failure) || local.starts != 0 {
		t.Fatalf("startup: %v, local starts=%d", err, local.starts)
	}
}

func TestLocalProcessLifecycleValidatesBeforeStarting(t *testing.T) {
	home := t.TempDir()
	local := &localRuntimeFixture{}
	owner := &lifecycleOwner{err: ErrNoDaemon}
	lifecycle := newProcessLifecycle(home, local, nil, owner, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
		t.Fatal("invalid request started a process")
		return wingd.LocalStatus{}, nil
	})
	if _, err := lifecycle.EnsureStarted(t.Context(), wingd.LocalStartOptions{Home: t.TempDir()}); err == nil {
		t.Fatal("accepted another home")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := lifecycle.EnsureStarted(ctx, wingd.LocalStartOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
