package main

import (
	"context"

	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func newLocalNativeServices(ctx context.Context, home string, owned *store.Store, binding runners.StoreBinding) (runners.NativeServices, error) {
	options, err := localSourceOptions(home, nil, secrets.NewMasker())
	if err != nil {
		return runners.NativeServices{}, err
	}
	return localcontroller.NativeServices(ctx, localcontroller.NativeConfig{
		Home: home, Version: installedVersion(), Store: binding.Tenant, Parent: owned, Bindings: owned,
		Principal: binding.Principal, Refresh: binding.Refresh, Renew: owned.RenewUnit, Source: options,
	})
}
