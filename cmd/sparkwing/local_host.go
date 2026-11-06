package main

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/source"
)

func localRunnerHosting(home string, options client.EngineOptions) runners.Hosting {
	hosting := runners.NativeHosting(runners.NativeHostingConfig{
		Home: home, Controller: options, Admission: nodehost.MachineAdmission(home, installedVersion()), SubmitAccept: procenv.SubmitAcceptAll(),
		Source: func(_ context.Context, unit controllerwire.ClaimedUnit, connection client.EngineOptions) (source.Options, error) {
			return localAwardSourceOptions(home, unit, connection)
		},
	})
	bind := hosting.Bind
	hosting.Bind = func(ctx context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (nodehost.NativeServices, capability.Principal, error) {
		services, principal, err := bind(ctx, unit, controller)
		if err != nil {
			return services, principal, err
		}
		err = bindLocalProfiles(ctx, home, options, unit, &services)
		return services, principal, err
	}
	return hosting
}

func bindLocalProfiles(ctx context.Context, home string, options client.EngineOptions, unit controllerwire.ClaimedUnit, services *nodehost.NativeServices) error {
	request := unit.Request.Request
	if request.Origin != runspec.OriginLocal {
		return nil
	}
	if profile := request.Options.CacheFrom.Value; profile != "" {
		snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
		if err != nil {
			return err
		}
		environment, err := snapshots.Load(ctx, request.Env)
		if err != nil {
			return err
		}
		cache, closeCache, err := openProfileCache(ctx, environment, profile, request.Git, request.Source.Location.LocalCheckout)
		if err != nil {
			return err
		}
		services.Storage.RemoteCache = cache
		closeHost := services.Close
		services.Close = func() error { return errors.Join(closeHost(), closeCache()) }
	}
	if unit.Unit == capability.UnitTrigger && request.Options.Publish.Value != "" {
		closeHost := services.Close
		services.Close = func() error {
			err := closeHost()
			finished := context.WithoutCancel(ctx)
			options.Bearer = nil
			if publishErr := publishLocalProfile(finished, home, options, request.Options.Publish.Value, unit.RunID); publishErr != nil {
				slog.Warn("local run completed but publication failed", "run_id", unit.RunID, "err", publishErr)
			}
			return err
		}
	}
	return nil
}
