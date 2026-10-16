package main

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newLocalNativeServices(ctx context.Context, home string, owned *store.Store, binding runners.StoreBinding, options client.EngineOptions) (_ runners.NativeServices, err error) {
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
	source, err := localSourceOptions(home, nil, secrets.NewMasker())
	if err != nil {
		return runners.NativeServices{}, err
	}
	services, err := localcontroller.NativeServices(ctx, localcontroller.WithClient(localcontroller.NativeConfig{
		Home: home, Version: installedVersion(), Store: binding.Tenant, Parent: owned, Bindings: owned,
		Principal: binding.Principal, Refresh: binding.Refresh, Renew: owned.RenewUnit, Source: source, Local: func(store.NodeRow) bool { return true },
	}, private))
	if err != nil {
		return runners.NativeServices{}, err
	}
	closeServices := services.Close
	services.Close = func() error { return errors.Join(closeServices(), private.Close()) }
	adopt := services.Config.Owner.AdoptClaim
	services.Config.Owner.AdoptClaim = func(ctx context.Context, claim store.ReconciledNode) error {
		if err := private.AdoptClaim(ctx, claim); err != nil {
			return err
		}
		return adopt(ctx, claim)
	}
	return services, nil
}
