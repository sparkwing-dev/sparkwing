package cluster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/internal/runners/launcher"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/localcontroller"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// ProductionRuntime composes installed commands with authenticated controller and native host services.
func ProductionRuntime(version string) Runtime {
	return Runtime{
		Runner: RunnerCLI{Factory: func(ctx context.Context, options RunnerOptions) (runners.RuntimeConfig, error) {
			home, err := wingd.HomeDir("")
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			engines, err := engine.NewResolver(engine.Config{CacheDirectory: filepath.Join(home, "engines")})
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			hosting := &productionHost{UnitHost: productionHosting(home, version, *options.Config.Controller)}
			installation, err := productionInstallation(options.ConfigPath, hosting)
			if err != nil {
				return runners.RuntimeConfig{}, err
			}
			return runners.RuntimeConfig{Installation: installation, Versions: engines, Target: releaseasset.Target{Binary: releaseasset.SparkwingRunner, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
				Hosting: hosting}, nil
		}},
		Bootstrap: BootstrapRuntime{
			Source: func(_ context.Context, options BootstrapOptions) (launcher.SourceInit, error) {
				return launcher.SourceInit{Bind: func(_ context.Context, award launcher.CloudJobAward) (source.Options, func() error, error) {
					access, err := client.NewSourceAccess(client.EngineOptions{URL: options.ControllerURL, Bearer: fixedBearer(string(award.Bootstrap))}, award.Binding.RunID, award.Source)
					return source.Options{Access: access, Base: procenv.Base{Ambient: os.Environ(), Masker: secrets.NewMasker()}}, nil, err
				}}, nil
			},
			Host: func(_ context.Context, options BootstrapOptions) (launcher.BootstrapHost, func() error, error) {
				home, err := wingd.HomeDir("")
				if err != nil {
					return launcher.BootstrapHost{}, nil, err
				}
				return launcher.BootstrapHost{Host: productionHosting(home, version, options.ControllerURL)}, nil, nil
			},
		},
	}
}

func fixedBearer(raw string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) { return raw, context.Cause(ctx) }
}

func productionHosting(home, version, url string) runners.Hosting {
	return runners.Hosting{ControllerURL: url, Bind: func(ctx context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (services runners.NativeServices, principal capability.Principal, err error) {
		options := client.EngineOptions{URL: url, Bearer: fixedBearer(unit.Capability)}
		var private *client.LocalHostClient
		if unit.Unit == capability.UnitNode {
			private, err = client.NewNodeHostClient(options, unit.RunID, unit.JobID)
		} else {
			private, err = client.NewLocalHostClient(options, unit.RunID)
		}
		if err != nil {
			return services, principal, err
		}
		closeResources := private.Close
		defer func() {
			if err != nil {
				err = errors.Join(err, closeResources())
			}
		}()
		refresh := func(ctx context.Context) (capability.Principal, error) {
			p, e := private.Principal(ctx)
			if e == nil {
				e = runners.ValidateAwardPrincipal(unit, p)
			}
			return p, e
		}
		principal, err = refresh(ctx)
		if err != nil {
			return services, principal, err
		}
		var processes *nodehost.NativeProcesses
		state, err := nodehost.NewRemoteHostStore(controller, private, unit.UnitID, func() bool { return processes != nil && processes.Computing(unit.RunID) })
		if err != nil {
			return services, principal, err
		}
		state.Finish = private.FinishRunAndUnit
		engines, err := engine.NewResolver(engine.Config{CacheDirectory: filepath.Join(home, "engines")})
		if err != nil {
			return services, principal, err
		}
		input, err := nodehost.NewRunInput(unit)
		if err != nil {
			return services, principal, err
		}
		spec, err := source.ForRequest(unit.Request)
		if input.Accepted != nil {
			spec, err = source.ForExecution(unit.Accepted.Spec)
		}
		if err != nil {
			return services, principal, err
		}
		access, err := client.NewSourceAccess(options, unit.RunID, spec)
		if err != nil {
			return services, principal, err
		}
		services, err = localcontroller.NativeServices(ctx, localcontroller.WithClient(localcontroller.NativeConfig{
			Home: home, Version: version, Store: state, Parent: private, Bindings: private, Principal: principal, Refresh: refresh,
			Source: source.Options{Engines: engines, Access: access, SDK: engine.GoSDK, Workspace: filepath.Join(home, "workspaces"), Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}, Base: procenv.Base{Ambient: os.Environ(), Masker: secrets.NewMasker()}},
			Renew: func(ctx context.Context, _ string, computing bool) (store.LeaseBeat, error) {
				return state.RenewOwner(ctx, store.Actor{}, computing)
			},
		}, private))
		if err != nil {
			return services, principal, err
		}
		processes = services.Config.Processes
		closeResources = func() error { return errors.Join(services.Close(), private.Close()) }
		originalClose := services.Close
		services.Close = func() error { return errors.Join(originalClose(), private.Close()) }
		if unit.Unit == capability.UnitTrigger {
			adopt := services.Config.Owner.AdoptClaim
			services.Config.Owner.AdoptClaim = func(ctx context.Context, claim store.ReconciledNode) error {
				if err := private.AdoptClaim(ctx, claim); err != nil {
					return err
				}
				return adopt(ctx, claim)
			}
		}
		return services, principal, nil
	}}
}

type productionHost struct {
	runners.UnitHost
	active atomic.Int64
}

func (h *productionHost) Host(ctx context.Context, unit controllerwire.ClaimedUnit) error {
	h.active.Add(1)
	defer h.active.Add(-1)
	return h.UnitHost.Host(ctx, unit)
}
