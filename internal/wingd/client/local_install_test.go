package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type installingUnits struct {
	wingd.ManagedUnitManager
	install func(context.Context, wingd.LocalStartOptions) error
	units   []wingd.ManagedUnit
}

func (u *installingUnits) InstallController(ctx context.Context, opts wingd.LocalStartOptions) error {
	return u.install(ctx, opts)
}

func (u *installingUnits) Inspect(context.Context) ([]wingd.ManagedUnit, error) { return u.units, nil }

type drainingInstallOwner struct {
	lifecycleOwner
	mu    sync.Mutex
	drain func(context.Context) error
}

func (o *drainingInstallOwner) Drain(ctx context.Context) error { return o.drain(ctx) }

func (o *drainingInstallOwner) Status(ctx context.Context) (wingd.LocalStatus, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.lifecycleOwner.Status(ctx)
}

func (o *drainingInstallOwner) setStatus(status wingd.LocalStatus) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.status = status
}

func TestLocalInstallJoinsOwnerBeforeActivationAndGatesStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		home := t.TempDir()
		local := &localRuntimeFixture{}
		draining, release, activated := make(chan struct{}), make(chan struct{}), make(chan struct{})
		owner := &drainingInstallOwner{lifecycleOwner: lifecycleOwner{status: wingd.LocalStatus{Running: true, Stay: true, PID: 72, Version: "v1"}}}
		owner.drain = func(context.Context) error {
			close(draining)
			<-release
			owner.setStatus(wingd.LocalStatus{})
			return nil
		}
		units := &installingUnits{}
		units.install = func(_ context.Context, opts wingd.LocalStartOptions) error {
			status, err := owner.Status(t.Context())
			if err != nil || status.Running || !opts.Stay {
				t.Error("service activated before the old owner joined or without stay")
			}
			units.units = []wingd.ManagedUnit{{Kind: wingd.ManagedController}}
			close(activated)
			return nil
		}
		lifecycle := newProcessLifecycle(home, local, units, owner, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
			t.Error("service handoff started an on-demand controller")
			return wingd.LocalStatus{}, errors.New("unexpected startup")
		})
		opts := wingd.LocalStartOptions{Home: home, Executable: filepath.Join(home, "sparkwing"), Version: "v2"}
		installed, started := make(chan error, 1), make(chan error, 1)
		go func() {
			status, err := lifecycle.InstallController(t.Context(), opts)
			if err == nil && (!status.Running || !status.Installed || !status.Stay || status.PID != 84) {
				err = errors.New("installation did not return the managed successor")
			}
			installed <- err
		}()
		<-draining
		releaseClaim, claimed, err := wingd.ClaimDaemonStart(home)
		if err != nil {
			t.Fatal(err)
		}
		if claimed {
			releaseClaim()
			t.Error("handoff left startup available to another process")
		}
		go func() { _, err := lifecycle.EnsureStarted(t.Context(), opts); started <- err }()
		synctest.Wait()
		select {
		case <-activated:
			t.Fatal("service started while its predecessor still owned the home")
		case <-started:
			t.Fatal("start bypassed the installation transition")
		default:
		}
		close(release)
		<-activated
		synctest.Wait()
		select {
		case <-installed:
			t.Fatal("installation returned before its successor became ready")
		case <-started:
			t.Fatal("start bypassed successor readiness")
		default:
		}
		owner.setStatus(wingd.LocalStatus{Running: true, Stay: true, PID: 84, Version: opts.Version})
		if err := <-installed; err != nil {
			t.Fatal(err)
		}
		if err := <-started; err != nil {
			t.Fatal(err)
		}
		if local.starts != 0 {
			t.Fatal("service handoff opened a local controller")
		}
		releaseClaim, claimed, err = wingd.ClaimDaemonStart(home)
		if err != nil || !claimed {
			t.Fatalf("handoff retained startup claim: claimed=%t error=%v", claimed, err)
		}
		releaseClaim()
	})
}

func TestLocalInstallRefusesUnverifiedSuccessorAndPreservesFailures(t *testing.T) {
	for _, mode := range []string{"drain", "activate", "same_pid", "zero_pid", "not_staying", "wrong_version", "missing_unit", "cancel_ready", "foreign_home"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			local := &localRuntimeFixture{}
			owner := &drainingInstallOwner{lifecycleOwner: lifecycleOwner{status: wingd.LocalStatus{Running: true, Stay: true, PID: 72, Version: "v1"}}}
			failure := errors.New("native boundary failed")
			drains, installs := 0, 0
			owner.drain = func(context.Context) error {
				drains++
				if mode == "drain" {
					return failure
				}
				owner.setStatus(wingd.LocalStatus{})
				return nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			units := &installingUnits{}
			units.install = func(_ context.Context, opts wingd.LocalStartOptions) error {
				installs++
				if mode == "activate" {
					return failure
				}
				units.units = []wingd.ManagedUnit{{Kind: wingd.ManagedController}}
				owner.status = wingd.LocalStatus{Running: true, Stay: true, PID: 84, Version: opts.Version}
				switch mode {
				case "same_pid":
					owner.status.PID = 72
				case "zero_pid":
					owner.status.PID = 0
				case "not_staying":
					owner.status.Stay = false
				case "wrong_version":
					owner.status.Version = "v1"
				case "missing_unit":
					units.units = nil
				case "cancel_ready":
					owner.status = wingd.LocalStatus{}
					cancel()
				}
				return nil
			}
			lifecycle := newProcessLifecycle(home, local, units, owner, func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error) {
				t.Fatal("failed service installation started a second controller")
				return wingd.LocalStatus{}, nil
			})
			opts := wingd.LocalStartOptions{Home: home, Executable: filepath.Join(home, "sparkwing"), Version: "v2"}
			if mode == "foreign_home" {
				opts.Home = t.TempDir()
			}
			if _, err := lifecycle.InstallController(ctx, opts); err == nil {
				t.Fatal("unverified service installation reported success")
			} else if mode == "activate" && (!errors.Is(err, failure) || !strings.Contains(err.Error(), "drain")) || mode == "drain" && !errors.Is(err, failure) || mode == "cancel_ready" && !errors.Is(err, context.Canceled) {
				t.Fatalf("installation lost boundary failure or handoff state: %v", err)
			}
			if mode == "drain" && installs != 0 || mode == "foreign_home" && (drains != 0 || installs != 0) || local.starts != 0 {
				t.Fatalf("invalid installation crossed boundary: drains=%d installs=%d opens=%d", drains, installs, local.starts)
			}
			release, claimed, err := wingd.ClaimDaemonStart(home)
			if err != nil || !claimed {
				t.Fatalf("failed installation retained startup claim: claimed=%t error=%v", claimed, err)
			}
			release()
		})
	}
}
