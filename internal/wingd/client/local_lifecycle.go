package client

import (
	"context"
	"errors"

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

type ownerRuntime struct {
	wingd.LocalRuntime
	owner        controllerOwner
	awaitManaged bool
}

func (r *ownerRuntime) AwaitReady(ctx context.Context) (wingd.LocalStatus, error) {
	if !r.awaitManaged {
		return r.LocalRuntime.AwaitReady(ctx)
	}
	return awaitLocalReady(ctx, r.Status)
}

func awaitLocalReady(ctx context.Context, query func(context.Context) (wingd.LocalStatus, error)) (wingd.LocalStatus, error) {
	wait := newRetry("wait for local controller readiness", 0)
	for {
		status, err := query(ctx)
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

func (r *ownerRuntime) Status(ctx context.Context) (wingd.LocalStatus, error) {
	status, err := r.LocalRuntime.Status(ctx)
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
	status, err := r.LocalRuntime.Status(ctx)
	if err != nil {
		return nil, err
	}
	// safety: a starting or draining local process retains ownership until its cleanup joins.
	if status.PID != 0 || status.Running {
		return r.LocalRuntime, nil
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
