package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	hosterr "github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/sparkwing/protocol/apierr"
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

const localRunnerStatusPoll = 100 * time.Millisecond

// safety: a controller that predates runner reporting leaves Runner empty, which must not refuse its runs.
func localRunnerUnavailable(ctx context.Context, status func(context.Context) (wingd.LocalStatus, error)) (*apierr.Error, error) {
	poll := time.NewTicker(localRunnerStatusPoll)
	defer poll.Stop()
	for {
		current, err := status(ctx)
		if err != nil {
			return nil, err
		}
		switch current.Runner {
		case wingd.LocalRunnerUnavailable:
			if current.RunnerError != nil {
				return current.RunnerError, nil
			}
			return &apierr.Error{Code: hosterr.Unavailable, Message: "the local runner cannot start", Hint: "run sparkwing daemon status"}, nil
		case wingd.LocalRunnerStarting:
		default:
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-poll.C:
		}
	}
}
