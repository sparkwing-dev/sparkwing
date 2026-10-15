package main

import (
	"context"
	"errors"
	"os/signal"

	"github.com/sparkwing-dev/sparkwing/internal/secrets"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/limits"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/runners"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runTriggerHost(args []string) error {
	fs := flag.NewFlagSet(orchestrator.TriggerHostVerb, flag.ContinueOnError)
	runID := fs.String("run", "", "the admitted run to claim and host")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runID == "" || fs.NArg() != 0 {
		return errors.New("usage: sparkwing " + orchestrator.TriggerHostVerb + " --run <run-id>")
	}
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	options, closeTransport, err := controllerOptions(ctx, "")
	if err != nil {
		return err
	}
	defer closeTransport()
	return hostLocalTrigger(ctx, options, *runID, resolveHostRuntime)
}

func hostLocalTrigger(ctx context.Context, options client.EngineOptions, runID string, resolve func(context.Context, client.EngineOptions) (runHostRuntime, error)) error {
	runtime, err := resolve(ctx, options)
	if err != nil {
		return err
	}
	if runtime.controller == nil || runtime.bind == nil {
		return &apierr.Error{Code: apierr.PolicyMissing, Message: "local trigger requires a shared host binding"}
	}
	award, err := runtime.controller.Claim(ctx, controllerwire.ClaimRequest{RunID: runID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: installedVersion()})
	if err != nil {
		return err
	}
	if award.Claim != nil && award.Claim.RunID != runID {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "named local claim returned another run"}
	}
	if award.Claim == nil {
		return &apierr.Error{Code: apierr.LockHeld, Message: "local trigger has no available owner claim"}
	}
	_, err = hostLocalAward(ctx, options, *award.Claim, runtime)
	return err
}

func hostLocalAward(ctx context.Context, options client.EngineOptions, award controllerwire.ClaimedUnit, runtime runHostRuntime) (completion nodehost.RunCompletion, err error) {
	masks := secrets.NewMasker()
	masks.Register(award.Capability)
	for _, value := range award.Request.Request.RequestedArgs {
		masks.Register(value)
	}
	if award.Accepted != nil {
		for _, value := range award.Accepted.Spec.Resolved.Args {
			masks.Register(value)
		}
	}
	defer func() {
		if err != nil {
			err = &maskedCLIError{err: err, masker: masks}
		}
	}()
	hosting := runners.Hosting{
		ControllerURL: options.URL, HTTP: options.HTTP, Lease: limits.DefaultLease, Interval: limits.Renew(limits.DefaultLease),
		NewController: client.NewEngineClient,
		PrepareRun: func(ctx context.Context, award controllerwire.ClaimedUnit, _ controllerwire.Controller) (nodehost.RunHost, nodehost.RunInput, func() error, error) {
			scoped := options
			scoped.Bearer = func(context.Context) (string, error) { return award.Capability, nil }
			session, err := runtime.bind(ctx, scoped, award)
			if session.host == nil || session.close == nil {
				err = errors.Join(err, &apierr.Error{Code: apierr.PolicyMissing, Message: "local hosting requires a shared host and cleanup"})
			}
			return completingRunHost{host: session.host, completion: &completion}, session.input, session.close, err
		},
	}
	err = hosting.Host(ctx, award)
	return completion, err
}

type completingRunHost struct {
	host       nodehost.RunHost
	completion *nodehost.RunCompletion
}

func (h completingRunHost) Run(ctx context.Context, input nodehost.RunInput) (completion nodehost.RunCompletion, err error) {
	completion, err = h.host.Run(ctx, input)
	*h.completion = completion
	return completion, err
}
