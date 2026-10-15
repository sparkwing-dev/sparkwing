package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type hereDependencies struct {
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
		credentials: localHereCredentials, version: installedVersion(), resolve: resolveHereRuntime,
		attach: func(ctx context.Context, options client.EngineOptions, run string, host func(context.Context) (nodehost.RunCompletion, error)) (nodehost.RunCompletion, error) {
			return attachHereRun(ctx, options, run, output, os.Stdout, host)
		},
	}
}

func resolveHereRuntime(context.Context, client.EngineOptions) (hereRuntime, error) {
	return hereRuntime{}, &apierr.Error{Code: apierr.Unavailable, Message: "here run hosting is unavailable in this build", Hint: "the shared run host, source, storage and lease keeper require a native binding"}
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
	response, err := runtime.controller.Claim(ctx, controllerwire.ClaimRequest{RunID: run, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: deps.version})
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
) (completion nodehost.RunCompletion, resultErr error) {
	input, err := hereRunInput(run, award)
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	ctx, cancel := award.HostContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	options.Bearer = func(context.Context) (string, error) { return award.Capability, nil }
	session, err := bind(ctx, options, award)
	defer func() {
		if session.close != nil {
			if err := session.close(); err != nil {
				resultErr = errors.Join(resultErr, incompleteHereHandoff(run, err))
			}
		}
	}()
	if err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	if session.host == nil || session.close == nil {
		return completion, incompleteHereHandoff(run, &apierr.Error{Code: apierr.Unavailable, Message: "here runtime returned no host or cleanup binding"})
	}
	if err := ctx.Err(); err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	session.input.RunID, session.input.SealedRequest, session.input.RequestHash, session.input.Accepted = input.RunID, input.SealedRequest, input.RequestHash, input.Accepted
	session.input.GrantedRunner = input.GrantedRunner
	completion, runErr := session.host.Run(ctx, session.input)
	if err := errors.Join(runErr, ctx.Err()); err != nil {
		return completion, incompleteHereHandoff(run, err)
	}
	if !isTerminalRunStatus(completion.Outcome) {
		return completion, incompleteHereHandoff(run, &apierr.Error{Code: apierr.ProtocolError, Message: "here host returned no terminal outcome"})
	}
	return completion, nil
}

func hereRunInput(run string, award controllerwire.ClaimedUnit) (nodehost.RunInput, error) {
	input := nodehost.RunInput{RunID: run, RequestHash: string(award.Request.Hash), GrantedRunner: award.Runner}
	input.GrantedRunner.Labels = slices.Clone(award.Runner.Labels)
	if award.Unit != capability.UnitTrigger || award.Purpose != capability.PurposeHere || award.Delivery != capability.DeliveryDirect || award.RunID != run || award.UnitID == "" || award.JobID != "" || award.Capability == "" || award.Binding.Kind != capability.BindingTrigger || award.Binding.RunID != run || award.Binding.Holder == "" || award.Binding.Generation <= 0 || !award.Request.Request.Options.Runner.Set || award.Request.Request.Options.Runner.Value.Kind != runspec.RunnerHere {
		return input, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid here owner award"}
	}
	hash, err := runspec.CanonicalHash(award.Request)
	if err != nil || award.Request.Version != 1 || award.Request.Request.Version != 1 || award.Request.RunID == "" || award.Request.Hash == "" || hash != award.Request.Hash {
		return input, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid here request seal"}
	}
	input.SealedRequest, err = json.Marshal(award.Request)
	if err != nil {
		return input, err
	}
	if award.Accepted != nil {
		accepted := award.Accepted
		_, sealErr := runspec.CheckSeal(accepted.Spec, accepted.Spec)
		planHash, planErr := runspec.CanonicalHash(accepted.Plan)
		if sealErr != nil || planErr != nil || planHash != accepted.PlanHash || accepted.Spec.Request.Hash != award.Request.Hash || accepted.Spec.PlanHash != accepted.PlanHash {
			return input, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned inconsistent here seals"}
		}
		body, err := json.Marshal(accepted.Spec)
		if err != nil {
			return input, err
		}
		input.Accepted = &nodehost.AcceptedRun{RunID: run, Spec: body, SpecHash: string(accepted.Spec.Hash), PlanHash: string(accepted.PlanHash)}
		decoder := json.NewDecoder(bytes.NewReader(accepted.Plan))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input.Accepted.Plan); err != nil {
			return input, &apierr.Error{Code: apierr.ProtocolError, Message: "controller returned an invalid accepted here plan"}
		}
	}
	return input, nil
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
