package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

func ensureLocalController(ctx context.Context, home string, resolve func(context.Context) (dashboardServices, error)) (status wingd.LocalStatus, err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("cannot start local controller: %w", err)
		}
	}()
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	services, err := resolve(ctx)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if services.lifecycle == nil {
		return wingd.LocalStatus{}, dashboardUnavailable("the local controller lifecycle")
	}
	home, err = wingd.HomeDir(home)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	bound, err := wingd.HomeDir(services.options.Home)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if filepath.Clean(home) != filepath.Clean(bound) {
		return wingd.LocalStatus{}, errors.New("local controller is bound to another home")
	}
	options := services.options
	options.Home = home
	status, err = services.lifecycle.EnsureStarted(ctx, options)
	if err != nil {
		return wingd.LocalStatus{}, err
	}
	if err := ctx.Err(); err != nil {
		return wingd.LocalStatus{}, err
	}
	if !status.Running || status.PID <= 0 || status.Version == "" {
		return wingd.LocalStatus{}, errors.New("local controller has not established API readiness")
	}
	return status, nil
}
