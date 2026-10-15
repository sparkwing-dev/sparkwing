package main

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func startLocalController(ctx context.Context, home string, resolve func(context.Context) (dashboardServices, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	services, err := resolve(ctx)
	if err != nil {
		return err
	}
	if services.lifecycle == nil {
		return dashboardUnavailable("the local controller lifecycle")
	}
	home, err = wingd.HomeDir(home)
	if err != nil {
		return err
	}
	bound, err := wingd.HomeDir(services.options.Home)
	if err != nil {
		return err
	}
	if filepath.Clean(home) != filepath.Clean(bound) {
		return errors.New("local controller is bound to another home")
	}
	options := services.options
	options.Home = home
	status, err := services.lifecycle.EnsureStarted(ctx, options)
	if err == nil && !status.Running {
		return errors.New("local controller stopped during startup")
	}
	return err
}
