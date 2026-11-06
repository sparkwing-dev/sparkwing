package runners

import (
	"context"
	"errors"
	"maps"

	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controller/client"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runstate"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/store"
)

// NativeHostingConfig composes execution through award-bound controller clients.
// Source receives the award's connection, including its private HTTP transport.
type NativeHostingConfig struct {
	Home          string
	Controller    client.EngineOptions
	Source        func(context.Context, controllerwire.ClaimedUnit, client.EngineOptions) (source.Options, error)
	Admission     *nodehost.LocalAdmission
	SubmitAccept  procenv.SubmitAccept
	PrepareSource bool
	// RequesterSecrets carries captured burst inputs for this hosting instance.
	RequesterSecrets map[string]string
}

// NativeHosting shares the native owner and job composition across runner placements.
func NativeHosting(cfg NativeHostingConfig) Hosting {
	return Hosting{ControllerURL: cfg.Controller.URL, HTTP: cfg.Controller.HTTP, Bind: func(ctx context.Context, unit controllerwire.ClaimedUnit, controller controllerwire.Controller) (services nodehost.NativeServices, principal capability.Principal, err error) {
		if cfg.Source == nil {
			return services, principal, errors.New("native hosting requires source options")
		}
		options := cfg.Controller
		options.Bearer, options.Launcher = func(ctx context.Context) (string, error) { return unit.Capability, context.Cause(ctx) }, false
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
				e = ValidateAwardPrincipal(unit, p)
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
		if unit.Request.Request.Burst != nil && cfg.RequesterSecrets != nil {
			state.LocalSecrets = maps.Clone(cfg.RequesterSecrets)
			if _, err := state.RenewOwner(ctx, runstate.Actor{}, false); err != nil {
				return services, principal, err
			}
		}
		sourceOptions, err := cfg.Source(ctx, unit, options)
		if err != nil {
			return services, principal, err
		}
		if sourceOptions.BundleTransport == nil && options.HTTP != nil {
			sourceOptions.BundleTransport = options.HTTP.Transport
		}
		submitted := cfg.SubmitAccept
		if unit.OwnerMachine {
			submitted = procenv.SubmitAcceptAll()
		}
		services, err = nodehost.NewNativeServices(ctx, nodehost.WithClient(nodehost.NativeConfig{
			Admission:    nodehost.ComposeStartGate(state, cfg.Admission, cfg.Admission != nil),
			SubmittedEnv: unit.SubmittedEnv, SubmitAccept: submitted,
			Home: cfg.Home, Store: state, Parent: private, Bindings: private, Principal: principal, Refresh: refresh, Local: store.OwnerCanExecute(principal),
			Source: sourceOptions,
			Renew: func(ctx context.Context, _ string, computing bool) (runstate.LeaseBeat, error) {
				return state.RenewOwner(ctx, runstate.Actor{}, computing)
			},
		}, private))
		if err != nil {
			return services, principal, err
		}

		processes = services.Config.Processes
		closeResources = func() error { return errors.Join(services.Close(), private.Close()) }
		if cfg.PrepareSource && (unit.Unit == capability.UnitPlan || unit.Unit == capability.UnitTrigger || unit.Unit == capability.UnitNode) {
			storage, bindErr := private.BindStorage(ctx, principal)
			if bindErr != nil {
				return services, principal, bindErr
			}
			services.Config.Source.Prepared = &source.Prepared{
				Bytes: storage.Bytes, Records: storage.Records, Authority: storage.Authority,
				Key:         blob.PreparedRecordKey(unit.Request.Request.Team, unit.RunID, storage.Authority.Repo, unit.Request.Request.Pipeline),
				RequestHash: string(unit.Request.Hash), Publish: unit.Unit != capability.UnitNode,
			}
		}
		originalClose := services.Close
		services.Close = func() error { return errors.Join(originalClose(), private.Close()) }

		if unit.Unit == capability.UnitTrigger {
			adopt := services.Config.Owner.AdoptClaim
			services.Config.Owner.AdoptClaim = func(ctx context.Context, claim runstate.ReconciledNode) error {
				if err := private.AdoptClaim(ctx, claim); err != nil {
					return err
				}
				return adopt(ctx, claim)
			}
		}
		return services, principal, nil
	}}
}
