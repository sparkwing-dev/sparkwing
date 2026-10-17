package main

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

type localOwnerState struct {
	*store.Tenant
	owner capability.AuthorityRef
}

func (s localOwnerState) ReadSnapshot(ctx context.Context, run string) (store.RunSnapshot, error) {
	return s.Tenant.ReadOwnerSnapshot(ctx, s.owner, run)
}

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
	source, err := hostSourceOptions(home, nil, secrets.NewMasker(), true)
	if err != nil {
		return runners.NativeServices{}, err
	}
	var state nodehost.NativeHostStore = binding.Tenant
	if binding.Principal.Capability.Unit == capability.UnitTrigger {
		state = localOwnerState{Tenant: binding.Tenant, owner: binding.Principal.Authority}
	}
	services, err := localcontroller.NativeServices(ctx, localcontroller.WithClient(localcontroller.NativeConfig{
		Home: home, Version: installedVersion(), Store: state, Parent: owned, Bindings: owned,
		Principal: binding.Principal, Refresh: binding.Refresh, Renew: owned.RenewUnit, Source: source, Local: store.OwnerCanExecute(binding.Principal),
	}, private))
	if err != nil {
		return runners.NativeServices{}, err
	}
	services.Config.Owner.Delegator = owned
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
