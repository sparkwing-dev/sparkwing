package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
)

type localOwnerState struct {
	*store.Tenant
	owner capability.AuthorityRef
}

func (s localOwnerState) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	return s.Tenant.ReadOwnerSnapshot(ctx, s.owner, run)
}

func newLocalNativeServices(ctx context.Context, home string, owned *store.Store, binding runners.StoreBinding, options client.EngineOptions) (runners.NativeServices, error) {
	return localNativeServices(ctx, home, owned, binding, options, localcontroller.NativeServices)
}

func localNativeServices(ctx context.Context, home string, owned *store.Store, binding runners.StoreBinding, options client.EngineOptions, build func(context.Context, localcontroller.NativeConfig) (runners.NativeServices, error)) (_ runners.NativeServices, err error) {
	options.Bearer = binding.Bearer
	var private *client.LocalHostClient
	fence := binding.Principal.Capability.Binding
	if binding.Principal.Capability.Unit == capability.UnitNode {
		private, err = client.NewNodeHostClient(options, fence.RunID, fence.NodeID)
	} else {
		private, err = client.NewLocalHostClient(options, fence.RunID)
	}
	if err != nil {
		return runners.NativeServices{}, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, private.Close())
		}
	}()
	source, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), true)
	if err != nil {
		return runners.NativeServices{}, err
	}
	var state nodehost.NativeHostStore = binding.Tenant
	if binding.Principal.Capability.Unit == capability.UnitTrigger {
		state = localOwnerState{Tenant: binding.Tenant, owner: binding.Principal.Authority}
	}
	services, err := build(ctx, localcontroller.WithClient(localcontroller.NativeConfig{
		Home: home, Version: installedVersion(), Store: state, Parent: owned.AsOperator(), Bindings: owned.AsOperator(),
		Principal: binding.Principal, Refresh: binding.Refresh, Renew: owned.AsOperator().RenewUnit, Source: source, Local: store.OwnerCanExecute(binding.Principal),
	}, private))
	if err != nil {
		return runners.NativeServices{}, err
	}
	services.Config.Owner.Delegator = owned.AsOperator()
	closeServices := services.Close
	services.Close = func() error { return errors.Join(closeServices(), private.Close()) }
	run, err := binding.Tenant.ReadRun(ctx, fence.RunID)
	if err != nil {
		return runners.NativeServices{}, errors.Join(err, services.Close())
	}
	if profile := run.Request.Request.Options.CacheFrom.Value; profile != "" && run.Request.Request.Origin == runspec.OriginLocal {
		snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(home, "environments"))
		if err != nil {
			return runners.NativeServices{}, errors.Join(err, services.Close())
		}
		environment, err := snapshots.Load(ctx, run.Request.Request.Env)
		if err != nil {
			return runners.NativeServices{}, errors.Join(err, services.Close())
		}
		cache, closeCache, err := openProfileCache(ctx, environment, profile, run.Request.Request.Git, run.Request.Request.Source.Location.LocalCheckout)
		if err != nil {
			return runners.NativeServices{}, errors.Join(err, services.Close())
		}
		services.Storage.RemoteCache = cache
		closeHost := services.Close
		services.Close = func() error { return errors.Join(closeHost(), closeCache()) }
	}
	if binding.Principal.Capability.Unit == capability.UnitTrigger {
		closeHost := services.Close
		services.Close = func() error {
			err := closeHost()
			finished := context.WithoutCancel(ctx)
			run, readErr := binding.Tenant.ReadRun(finished, fence.RunID)
			if readErr == nil && run.FinishedAt != nil && run.Request.Request.Options.Publish.Value != "" {
				local := options
				local.Bearer = nil
				if publishErr := publishLocalProfile(finished, home, local, run.Request.Request.Options.Publish.Value, run.ID); publishErr != nil {
					slog.Warn("local run completed but publication failed", "run_id", run.ID, "err", publishErr)
				}
			}
			return err
		}
	}
	adopt := services.Config.Owner.AdoptClaim
	services.Config.Owner.AdoptClaim = func(ctx context.Context, claim store.ReconciledNode) error {
		if err := private.AdoptClaim(ctx, claim); err != nil {
			return err
		}
		return adopt(ctx, claim)
	}
	return services, nil
}
