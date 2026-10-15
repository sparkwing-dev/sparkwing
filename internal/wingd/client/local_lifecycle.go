package client

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// NewLocalLifecycle starts the controller detached, reserving runtime for standalone execution.
func NewLocalLifecycle(home string, runtime wingd.LocalRuntime, units wingd.ManagedUnitManager) (wingd.LocalLifecycle, error) {
	if runtime == nil {
		return nil, errors.New("wingd/client: local controller runtime is required")
	}
	home, err := wingd.HomeDir(home)
	if err != nil {
		return nil, err
	}
	return newProcessLifecycle(home, runtime, units, localOwnerClient{home: home}, ensureLocalProcess), nil
}

type controllerOwner interface {
	Status(context.Context) (wingd.LocalStatus, error)
	SetStay(context.Context, bool) error
	EnsureDashboard(context.Context, string) error
	Drain(context.Context) error
}

type localOwnerClient struct{ home string }

func (o localOwnerClient) Status(ctx context.Context) (wingd.LocalStatus, error) {
	return QueryLocalController(ctx, o.home)
}

func (o localOwnerClient) SetStay(ctx context.Context, stay bool) error {
	_, err := SetLocalStay(ctx, o.home, stay)
	return err
}

func (o localOwnerClient) EnsureDashboard(ctx context.Context, address string) error {
	return StartLocalDashboard(ctx, o.home, address)
}

func (o localOwnerClient) Drain(ctx context.Context) error {
	return Stop(ctx, Options{Home: o.home})
}

type ownerLifecycle struct {
	wingd.LocalLifecycle
	home string
}

func newOwnerLifecycle(home string, runtime wingd.LocalRuntime, units wingd.ManagedUnitManager, owner controllerOwner) *ownerLifecycle {
	return &ownerLifecycle{LocalLifecycle: wingd.NewLocalLifecycle(&ownerRuntime{local: runtime, owner: owner}, units), home: home}
}

func (l *ownerLifecycle) options(opts wingd.LocalStartOptions) (wingd.LocalStartOptions, error) {
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

func (l *ownerLifecycle) EnsureStarted(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	opts, err := l.options(opts)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	return l.LocalLifecycle.EnsureStarted(ctx, opts)
}

func (l *ownerLifecycle) StartDashboard(ctx context.Context, opts wingd.LocalStartOptions) (wingd.LocalStatus, error) {
	opts, err := l.options(opts)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	return l.LocalLifecycle.StartDashboard(ctx, opts)
}

type ownerRuntime struct {
	local wingd.LocalRuntime
	owner controllerOwner
}

func (r *ownerRuntime) Run(ctx context.Context, opts wingd.LocalStartOptions) error {
	return r.local.Run(ctx, opts)
}

func (r *ownerRuntime) AwaitReady(ctx context.Context) (wingd.LocalStatus, error) {
	return r.local.AwaitReady(ctx)
}

func (r *ownerRuntime) Status(ctx context.Context) (wingd.LocalStatus, error) {
	status, err := r.local.Status(ctx)
	if err != nil || status.PID != 0 || status.Running {
		return status, err
	}
	status, err = r.owner.Status(ctx)
	if errors.Is(err, ErrNoDaemon) {
		return wingd.LocalStatus{}, nil
	}
	return status, err
}

func (r *ownerRuntime) target(ctx context.Context) (controllerOwner, error) {
	status, err := r.local.Status(ctx)
	if err != nil {
		return nil, err
	}
	// safety: a starting or draining local process retains ownership until its cleanup joins.
	if status.PID != 0 || status.Running {
		return r.local, nil
	}
	return r.owner, nil
}

func (r *ownerRuntime) SetStay(ctx context.Context, stay bool) error {
	owner, err := r.target(ctx)
	if err != nil {
		return err
	}
	return owner.SetStay(ctx, stay)
}

func (r *ownerRuntime) EnsureDashboard(ctx context.Context, address string) error {
	owner, err := r.target(ctx)
	if err != nil {
		return err
	}
	return owner.EnsureDashboard(ctx, address)
}

func (r *ownerRuntime) Drain(ctx context.Context) error {
	owner, err := r.target(ctx)
	if err != nil {
		return err
	}
	err = owner.Drain(ctx)
	if errors.Is(err, ErrNoDaemon) {
		return nil
	}
	return err
}
