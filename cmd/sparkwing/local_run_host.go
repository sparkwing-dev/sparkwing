package main

import (
	"context"
	"errors"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func bindLocalRunHost(ctx context.Context, home string, options client.EngineOptions, award controllerwire.ClaimedUnit) (_ hereHostSession, err error) {
	if award.Unit != capability.UnitTrigger || award.Capability == "" {
		return hereHostSession{}, localRunProtocolError("local host requires its trigger award")
	}
	options.Bearer = func(context.Context) (string, error) { return award.Capability, nil }
	controller, err := client.NewEngineClient(options)
	if err != nil {
		return hereHostSession{}, err
	}
	private, err := client.NewLocalHostClient(options, award.RunID)
	if err != nil {
		return hereHostSession{}, err
	}
	closeResources := private.Close
	defer func() {
		if err != nil {
			err = errors.Join(err, closeResources())
		}
	}()
	refresh := func(ctx context.Context) (capability.Principal, error) {
		principal, err := private.Principal(ctx)
		if err == nil {
			err = runners.ValidateAwardPrincipal(award, principal)
		}
		return principal, err
	}
	principal, err := refresh(ctx)
	if err != nil {
		return hereHostSession{}, err
	}
	var host *nodehost.OwnerHost
	state, err := nodehost.NewRemoteHostStore(controller, private, award.UnitID, func() bool {
		return host != nil && host.Computing != nil && host.Computing(award.RunID)
	})
	if err != nil {
		return hereHostSession{}, err
	}
	state.Finish = private.FinishRunAndUnit
	source, err := localSourceOptions(home, nil, secrets.NewMasker())
	if err != nil {
		return hereHostSession{}, err
	}
	engines, ok := source.Engines.(*engine.Resolver)
	if !ok {
		return hereHostSession{}, errors.New("local source has no native engine resolver")
	}
	services, err := localcontroller.NativeServices(ctx, localcontroller.WithClient(localcontroller.NativeConfig{
		Home: home, Version: installedVersion(), Store: state, Parent: private, Bindings: private,
		Principal: principal, Refresh: refresh, Source: source, Local: func(store.NodeRow) bool { return true },
		Renew: func(ctx context.Context, _ string, computing bool) (store.LeaseBeat, error) {
			return state.RenewOwner(ctx, store.Actor{}, computing)
		},
	}, private))
	if err != nil {
		return hereHostSession{}, err
	}
	closeResources = func() error { return errors.Join(services.Close(), private.Close()) }
	adopt := services.Config.Owner.AdoptClaim
	services.Config.Owner.AdoptClaim = func(ctx context.Context, claim store.ReconciledNode) error {
		if err := private.AdoptClaim(ctx, claim); err != nil {
			return err
		}
		return adopt(ctx, claim)
	}
	host, _, _, err = nodehost.NewNativeHost(services.Config)
	if err != nil {
		return hereHostSession{}, err
	}
	return hereHostSession{host: host, close: closeResources, input: nodehost.RunInput{
		Storage: services.Storage, Engines: engines,
		Owner: store.Actor{Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix},
	}}, nil
}
