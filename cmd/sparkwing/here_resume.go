package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	goruntime "runtime"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/match"
)

type hereDependencies struct {
	admitted    func(controllerwire.CreateRunResponse) error
	capacity    func(context.Context) (match.Resources, match.Resources, error)
	credentials func() (hereCredentials, error)
	resolve     func(context.Context, client.EngineOptions) (hereRuntime, error)
	version     string
	attach      func(context.Context, client.EngineOptions, string, func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error)
}

type hereRuntime struct {
	controller controllerwire.Controller
	bind       func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error)
}

type hereHostSession struct {
	host  nodehost.RunHost
	input nodehost.RunInput
	close func() error
}

func defaultHereDependencies(output string) hereDependencies {
	return hereDependencies{
		capacity: func(ctx context.Context) (match.Resources, match.Resources, error) {
			home, err := wingd.HomeDir("")
			if err != nil {
				return match.Resources{}, match.Resources{}, err
			}
			return observeBurstCapacity(ctx, home)
		},
		credentials: localHereCredentials, version: installedVersion(), resolve: resolveHereRuntime,
		attach: func(ctx context.Context, options client.EngineOptions, run string, host func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error) {
			return attachHereRun(ctx, options, run, output, os.Stdout, host)
		},
	}
}

func resolveHereRuntime(ctx context.Context, options client.EngineOptions) (hereRuntime, error) {
	if err := ctx.Err(); err != nil {
		return hereRuntime{}, err
	}
	controller, err := client.NewEngineClient(options)
	if err != nil {
		return hereRuntime{}, err
	}
	return hereRuntime{controller: controller, bind: func(ctx context.Context, options client.EngineOptions, award controllerwire.ClaimedUnit) (hereHostSession, error) {
		home, err := wingd.HomeDir("")
		if err != nil {
			return hereHostSession{}, err
		}
		return bindLocalRunHost(ctx, home, options, award)
	}}, nil
}

func handoffHereRun(ctx context.Context, options client.EngineOptions, response controllerwire.CreateRunResponse, deps hereDependencies) error {
	if response.Replayed && response.HereBearer != "" {
		return incompleteHereHandoff(response.RunID, &apierr.Error{Code: apierr.ProtocolError, Message: "controller replay returned one-time here authority"})
	}
	store, err := deps.credentials()
	if err != nil {
		return incompleteHereHandoff(response.RunID, err)
	}
	store = store.forController(options)
	if response.HereBearer != "" {
		if err := store.save(options.URL, response.RunID, response.HereBearer); err != nil {
			return incompleteHereHandoff(response.RunID, err)
		}
	}
	if deps.admitted != nil {
		if err := deps.admitted(response); err != nil {
			return incompleteHereHandoff(response.RunID, err)
		}
	}
	completion, err := resumeAttachedHereRun(ctx, options, response.RunID, deps)
	if err != nil {
		return err
	}
	return statusExitCode(completion.Outcome)
}

func resumeAttachedHereRun(ctx context.Context, options client.EngineOptions, run string, deps hereDependencies) (nodehost.RunCompletion, error) {
	host := func(ctx context.Context) (nodehost.RunCompletion, error) {
		return resumeHereRun(ctx, options, run, deps)
	}
	if deps.attach != nil {
		return deps.attach(ctx, options, run, host)
	}
	return host(ctx)
}

func incompleteHereHandoff(run string, cause error) error {
	return exitError(3, fmt.Errorf("here handoff for run %s is incomplete: %w", run, cause))
}

func resumeHereRun(ctx context.Context, options client.EngineOptions, run string, deps hereDependencies) (completion nodehost.RunCompletion, resultErr error) {
	if err := ctx.Err(); err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	store, err := deps.credentials()
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	store = store.forController(options)
	bearer, err := store.load(options.URL, run)
	if err != nil {
		return completion, incompleteHereHandoff(run, fmt.Errorf("load retained authority: %w; a lost bearer requires a fresh trigger request", err))
	}
	masker := secrets.NewMasker()
	masker.Register(bearer)
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	options.Bearer = func(context.Context) (string, error) { return bearer, nil }
	options.Launcher = false
	runtime, err := deps.resolve(ctx, options)
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	if runtime.controller == nil || runtime.bind == nil {
		return completion, incompleteHereHandoff(run, &apierr.Error{Code: apierr.Unavailable, Message: "here runtime has no controller or host binding"})
	}
	if err := ctx.Err(); err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	claim := controllerwire.ClaimRequest{RunID: run, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: deps.version, AvailableCores: float64(goruntime.NumCPU()), OS: goruntime.GOOS, Arch: goruntime.GOARCH}
	if deps.capacity != nil {
		capacity, available, err := deps.capacity(ctx)
		if err != nil {
			return completion, incompleteHereHandoff(run, err)
		}
		claim.Capacity = &capacity
		claim.AvailableCores = available.Cores
		claim.AvailableMemoryBytes = available.MemoryBytes
	}
	response, err := runtime.controller.Claim(ctx, claim)
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	if response.Claim == nil {
		return completion, incompleteHereHandoff(run, &apierr.Error{Code: apierr.LockHeld, Message: "here run has no available owner claim"})
	}
	award := *response.Claim
	masker.Register(award.Capability)
	for _, value := range award.Request.Request.RequestedArgs {
		masker.Register(value)
	}
	if award.Accepted != nil {
		for _, value := range award.Accepted.Spec.Resolved.Args {
			masker.Register(value)
		}
	}
	return runHereAward(ctx, options, run, award, runtime.bind)
}

func runHereAward(ctx context.Context, options client.EngineOptions, run string, award controllerwire.ClaimedUnit,
	bind func(context.Context, client.EngineOptions, controllerwire.ClaimedUnit) (hereHostSession, error),
) (completion nodehost.RunCompletion, err error) {
	input, err := hereRunInput(run, award)
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	completion, err = runOwnerSession(ctx, award, input, func(ctx context.Context, award controllerwire.ClaimedUnit) (hereHostSession, error) {
		options.Bearer = func(context.Context) (string, error) { return award.Capability, nil }
		return bind(ctx, options, award)
	})
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	return completion, nil
}

func hereRunInput(run string, award controllerwire.ClaimedUnit) (nodehost.RunInput, error) {
	if !award.Request.Request.Options.Runner.Set || award.Request.Request.Options.Runner.Value.Kind != runspec.RunnerHere {
		return nodehost.RunInput{}, localRunProtocolError("controller returned a different here runner selection")
	}
	return ownerRunInput(run, award, capability.PurposeHere)
}

func runHereResumeCommand(ctx context.Context, flags triggerFlags, out io.Writer,
	resolve func(context.Context, string) (client.EngineOptions, func(), error),
	connect func(client.EngineOptions) (controllerwire.Controller, error),
	resume func(context.Context, client.EngineOptions, string) (nodehost.RunCompletion, error),
) error {
	if err := ctx.Err(); err != nil {
		return incompleteHereHandoff(flags.resume, err)
	}
	options, closeTransport, err := resolve(ctx, flags.profile)
	if err != nil {
		return err
	}
	defer closeTransport()
	controller, err := connect(options)
	if err != nil {
		return err
	}
	detail, err := controller.ReadRun(ctx, flags.resume)
	if err != nil {
		return err
	}
	if detail.ID == "" {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned no canonical run identity"}
	}
	completion, err := resume(ctx, options, detail.ID)
	if err != nil {
		return err
	}
	result := struct {
		RunID   string `json:"run_id"`
		Outcome string `json:"outcome"`
	}{detail.ID, completion.Outcome}
	if flags.output == "json" {
		err = json.NewEncoder(out).Encode(result)
	} else if flags.output == "plain" {
		_, err = fmt.Fprintln(out, result.RunID)
	} else {
		_, err = fmt.Fprintf(out, "run %s: %s\n", result.RunID, result.Outcome)
	}
	return errors.Join(statusExitCode(completion.Outcome), err)
}
