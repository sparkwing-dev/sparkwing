package main

import (
	"context"
	"errors"
	"os"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
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
	if award.Request.Request.Burst != nil {
		if len(award.Request.Request.LocalSecretNames) > 0 {
			state.LocalSecrets = map[string]string{}
			for _, name := range award.Request.Request.LocalSecretNames {
				value, present := os.LookupEnv(name)
				if !present {
					return hereHostSession{}, errors.New("requester local secret is unavailable on resume")
				}
				state.LocalSecrets[name] = value
			}
			if _, err := state.RenewOwner(ctx, store.Actor{}, false); err != nil {
				return hereHostSession{}, err
			}
		}

		state.Available = func(ctx context.Context) (*match.Resources, error) {
			_, available, err := observeBurstCapacity(ctx, home)
			return &available, err
		}
	}
	sourceOptions, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), award.Request.Request.Source.Location.LocalCheckout != "")
	if err != nil {
		return hereHostSession{}, err
	}
	if award.Request.Request.Source.Location.LocalCheckout == "" {
		sourceOptions.Base.OwnerMachine, sourceOptions.GitAccess = award.OwnerMachine, gitAccess(award.OwnerMachine)
		spec, err := source.ForRequest(award.Request)
		if err != nil {
			return hereHostSession{}, err
		}
		sourceOptions.Access, err = client.NewSourceAccess(options, award.RunID, spec)
		if err != nil {
			return hereHostSession{}, err
		}
		if options.HTTP != nil {
			sourceOptions.BundleTransport = options.HTTP.Transport
		}
	}
	engines, ok := sourceOptions.Engines.(*engine.Resolver)
	if !ok {
		return hereHostSession{}, errors.New("local source has no native engine resolver")
	}
	admission, err := nodehost.MachineAdmission(home, installedVersion())
	if err != nil {
		return hereHostSession{}, err
	}
	if options.URL != wingdclient.APIBaseURL {
		admission.Controller = options.URL
	}
	services, err := nodehost.NewNativeServices(ctx, nodehost.WithClient(nodehost.NativeConfig{
		Admission:    nodehost.ComposeNativeAdmission(nil, admission, true),
		SubmittedEnv: award.SubmittedEnv, SubmitAccept: procenv.SubmitAcceptAll(),
		Home: home, Store: state, Parent: private, Bindings: private,
		Principal: principal, Refresh: refresh, Source: sourceOptions, Local: store.OwnerCanExecute(principal),
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
