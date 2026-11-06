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
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"

	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
)

func bindLocalRunHost(ctx context.Context, home string, options client.EngineOptions, award controllerwire.ClaimedUnit) (_ hereHostSession, err error) {
	if award.Unit != capability.UnitTrigger || award.Capability == "" {
		return hereHostSession{}, localRunProtocolError("local host requires its trigger award")
	}
	var requesterSecrets map[string]string
	if award.Request.Request.Burst != nil && len(award.Request.Request.LocalSecretNames) > 0 {
		requesterSecrets = map[string]string{}
		for _, name := range award.Request.Request.LocalSecretNames {
			value, present := os.LookupEnv(name)
			if !present {
				return hereHostSession{}, errors.New("requester local secret is unavailable on resume")
			}
			requesterSecrets[name] = value
		}
	}
	options.Bearer = func(ctx context.Context) (string, error) { return award.Capability, context.Cause(ctx) }
	controller, err := client.NewEngineClient(options)
	if err != nil {
		return hereHostSession{}, err
	}
	admission := nodehost.MachineAdmission(home, installedVersion())
	if options.URL != wingdclient.APIBaseURL {
		admission.Controller = options.URL
	}
	hosting := runners.NativeHosting(runners.NativeHostingConfig{
		Home: home, Controller: options, Admission: admission, SubmitAccept: procenv.SubmitAcceptAll(), RequesterSecrets: requesterSecrets,
		Source: func(_ context.Context, unit controllerwire.ClaimedUnit, connection client.EngineOptions) (source.Options, error) {
			return localAwardSourceOptions(home, unit, connection)
		},
	})
	services, principal, err := hosting.Bind(ctx, award, controller)
	defer func() {
		if err != nil && services.Close != nil {
			err = errors.Join(err, services.Close())
		}
	}()
	if err != nil {
		return hereHostSession{}, err
	}
	engines, ok := services.Config.Source.Engines.(*engine.Resolver)
	if !ok {
		return hereHostSession{}, errors.New("local source has no native engine resolver")
	}
	var host *nodehost.OwnerHost
	host, _, _, err = nodehost.NewNativeHost(services.Config)
	if err != nil {
		return hereHostSession{}, err
	}
	return hereHostSession{host: host, close: services.Close, input: nodehost.RunInput{
		Storage: services.Storage, Engines: engines,
		Owner: runstate.Actor{Authority: principal.Authority, CapabilityPrefix: principal.Capability.Prefix},
	}}, nil
}

func localAwardSourceOptions(home string, award controllerwire.ClaimedUnit, options client.EngineOptions) (source.Options, error) {
	local := award.Request.Request.Source.Location.LocalCheckout != ""
	sourceOptions, err := hostSourceOptions(home, os.Environ(), secrets.NewMasker(), local)
	if err != nil {
		return source.Options{}, err
	}
	if !local {
		sourceOptions.Base.OwnerMachine, sourceOptions.GitAccess = award.OwnerMachine, gitAccess(award.OwnerMachine)
		spec, err := source.ForRequest(award.Request)
		if err != nil {
			return source.Options{}, err
		}
		sourceOptions.Access, err = client.NewSourceAccess(options, award.RunID, spec)
		if err != nil {
			return source.Options{}, err
		}
	}
	return sourceOptions, nil
}
