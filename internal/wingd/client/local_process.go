package client

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type processLifecycle struct {
	wingd.LocalLifecycle
	control *ownerRuntime
	home    string
	start   func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error)
	gate    chan struct{}
}

func newProcessLifecycle(home string, runtime wingd.LocalRuntime, units wingd.ManagedUnitManager, owner controllerOwner, start func(context.Context, wingd.LocalStartOptions) (wingd.LocalStatus, error)) *processLifecycle {
	control := &ownerRuntime{LocalRuntime: runtime, owner: owner}
	return &processLifecycle{
		LocalLifecycle: wingd.NewLocalLifecycle(control, units),
		control:        control, home: home, start: start, gate: make(chan struct{}, 1),
	}
}

func (l *processLifecycle) options(opts wingd.LocalStartOptions) (wingd.LocalStartOptions, error) {
	if opts.Home == "" {
		opts.Home = l.home
	}
	home, err := wingd.HomeDir(opts.Home)
	if err != nil {
		return opts, err
	}
	if filepath.Clean(home) != filepath.Clean(l.home) {
		return opts, errors.New("wingd/client: lifecycle is bound to another home")
	}
	opts.Home = home
	return opts, nil
}

func (l *processLifecycle) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *processLifecycle) EnsureStarted(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	if err := l.lock(ctx); err != nil {
		return wingd.LocalStatus{}, err
	}
	defer func() { <-l.gate }()
	return l.ensure(ctx, opts)
}

func (l *processLifecycle) ensure(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	opts, err := l.options(opts)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	local, err := l.control.LocalRuntime.Status(ctx)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if opts.Standalone || local.PID != 0 || local.Running {
		return l.LocalLifecycle.EnsureStarted(ctx, opts)
	}
	status, err := l.control.owner.Status(ctx)
	if errors.Is(err, ErrNoDaemon) || (err == nil && !status.Running) {
		status, err = l.start(ctx, opts)
	}
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if !status.Running {
		return wingd.LocalStatus{}, errors.New("wingd/client: controller stopped during startup")
	}
	if opts.Stay && !status.Stay {
		if err := l.control.owner.SetStay(ctx, true); err != nil {
			return wingd.LocalStatus{}, err
		}
	}
	status, err = l.Status(ctx)
	if err == nil && (!status.Running || (opts.Stay && !status.Stay)) {
		err = errors.New("wingd/client: controller stopped during startup or stay promotion")
	}
	return status, err
}

func (l *processLifecycle) StartDashboard(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	if err := l.lock(ctx); err != nil {
		return wingd.LocalStatus{}, err
	}
	defer func() { <-l.gate }()
	status, err := l.ensure(ctx, opts)
	if err != nil || opts.DashboardAddress == "" {
		return status, err
	}
	if err := l.control.EnsureDashboard(ctx, opts.DashboardAddress); err != nil {
		return wingd.LocalStatus{}, err
	}
	status, err = l.Status(ctx)
	if err == nil && !status.Running {
		err = errors.New("wingd/client: controller stopped during dashboard startup")
	}
	return status, err
}

func (l *processLifecycle) Stop(ctx context.Context) error {
	if err := l.lock(ctx); err != nil {
		return err
	}
	defer func() { <-l.gate }()
	return l.LocalLifecycle.Stop(ctx)
}

func ensureLocalProcess(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	if opts.Standalone || !filepath.IsAbs(opts.Executable) || strings.TrimSpace(opts.Version) == "" {
		return wingd.LocalStatus{}, errors.New("wingd/client: detached startup requires an absolute executable and version")
	}
	options := Options{Home: opts.Home, Version: opts.Version, NoTakeover: true, Spawn: func(home, version string) error {
		return spawnDetached(opts.Executable, home, version)
	}}
	ctx, cancel := context.WithTimeout(ctx, daemonStartupBudget(options))
	defer cancel()
	cl, err := EnsureDaemon(ctx, options)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if err := cl.Close(); err != nil {
		return wingd.LocalStatus{}, err
	}
	wait := newRetry("wait for local controller readiness", 0)
	for {
		status, err := QueryLocalController(ctx, opts.Home)
		if err == nil && status.Running {
			return status, nil
		}
		if err != nil && !errors.Is(err, ErrNoDaemon) {
			return wingd.LocalStatus{}, err
		}
		if err := wait.wait(ctx, err); err != nil {
			return wingd.LocalStatus{}, err
		}
	}
}
