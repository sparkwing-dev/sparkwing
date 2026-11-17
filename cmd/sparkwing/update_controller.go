package main

import (
	"context"
	"io"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

type updateLocalUnits struct {
	wingd.ManagedUnitManager
	services dashboardServices
}

func (u updateLocalUnits) Refresh(ctx context.Context, executable, version string) error {
	if err := u.ManagedUnitManager.Refresh(ctx, executable, version); err != nil {
		return err
	}
	installed, err := u.ManagedUnitManager.ControllerInstalled(ctx)
	if err != nil || installed {
		return err
	}
	u.services.options.Executable, u.services.options.Version = executable, version
	return runDaemonLifecycleCommand(ctx, clisurface.Invocation{Command: clisurface.Command{Path: "sparkwing daemon restart"}}, io.Discard,
		func(context.Context) (dashboardServices, error) { return u.services, nil })
}
