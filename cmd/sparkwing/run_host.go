package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	runtimehost "runtime"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
)

type localRunOptions struct {
	standaloneReason, stateDB, environmentFile string
}

type localRunRuntime struct {
	controller  controllerwire.Controller
	prepare     func(context.Context, string, runFlags) (localRunPreparation, error)
	bind        func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error)
	close       func() error
	remoteCache blob.RemoteCache
}

type localRunDependencies struct {
	resolve func(context.Context, localRunOptions) (localRunRuntime, error)
	version string
}

func runLocalCommand(ctx context.Context, pipeline string, flags runFlags, args []string, out io.Writer, deps localRunDependencies) (resultErr error) {
	masker := secrets.NewMasker()
	for _, token := range args {
		masker.Register(token)
		if _, value, found := strings.Cut(token, "="); found {
			masker.Register(value)
		}
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	if err := validateLocalRunFlags(flags); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	runtime, err := deps.resolve(ctx, localRunOptions{})
	if err != nil {
		return err
	}
	if runtime.close != nil {
		defer func() { resultErr = errors.Join(resultErr, runtime.close()) }()
	}
	if runtime.controller == nil || runtime.prepare == nil || runtime.close == nil || (!flags.detached && runtime.bind == nil) {
		return &apierr.Error{Code: apierr.Unavailable, Message: "local run integration has incomplete intake or host bindings"}
	}
	prepared, err := runtime.prepare(ctx, pipeline, flags)
	if err != nil {
		return err
	}
	request, err := prepareLocalRunRequest(ctx, prepared, flags, args)
	if err != nil {
		return err
	}
	if request.Request.Pipeline != pipeline || request.Request.Origin != runspec.OriginLocal || request.Request.Mode != runspec.IntakeExecute || request.Request.Dispatch != runspec.DispatchHost {
		return localRunProtocolError("local preparation returned an invalid intake request")
	}
	if flags.idempotencyKey != "" {
		request.IdempotencyKey = flags.idempotencyKey
	} else if request.IdempotencyKey == "" {
		request.IdempotencyKey = rand.Text()
	}
	for _, value := range request.Request.RequestedArgs {
		masker.Register(value)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	handlePath := ""
	if flags.runHandleFile != "" {
		handlePath, err = filepath.Abs(flags.runHandleFile)
		if err != nil {
			return err
		}
		release, err := orchestrator.ReserveRunHandle(handlePath)
		if err != nil {
			return err
		}
		defer release()
	}
	if request.Request.Options.CacheFrom.Value != "" && runtime.remoteCache == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "run: remote cache requires an authorized read-only storage binding before intake"}
	}
	if request.Request.Options.Publish.Value != "" {
		return &apierr.Error{Code: apierr.Unavailable, Message: "run: publication requires the controller's authorized result and log publisher before intake"}
	}
	response, err := runtime.controller.CreateRun(ctx, request)
	masker.Register(response.HereBearer)
	for _, value := range response.Request.Request.RequestedArgs {
		masker.Register(value)
	}
	if err != nil {
		return err
	}
	if err := validateLocalIntake(response, pipeline); err != nil {
		return err
	}
	if handlePath != "" {
		if err := orchestrator.PublishRunHandle(handlePath, orchestrator.NewRunHandle(response.RunID, pipeline, "pending")); err != nil {
			return fmt.Errorf("run %s is admitted but its handle could not be published: %w", response.RunID, err)
		}
	}
	if flags.detached {
		return errors.Join(emitLocalRunReceipt(out, response, request.Request.Options.Output.Value), ctx.Err())
	}
	completion, err := hostLocalRun(ctx, runtime, response.RunID, response.Request.Hash, deps.version)
	if err != nil {
		return fmt.Errorf("run %s is admitted: %w", response.RunID, err)
	}
	return statusExitCode(completion.Outcome)
}

func validateLocalRunFlags(flags runFlags) error {
	if flags.parseErr != nil {
		return flags.parseErr
	}
	if flags.prioritySet || flags.priority != "" {
		priority, err := validatePriorityFlag(flags.priority)
		if err != nil {
			return err
		}
		if priority == "front" || priority == "back" {
			return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "run: relative priority requires controller queue resolution before sealing"}
		}
	}
	for _, option := range []struct {
		name string
		set  bool
	}{
		{"sw-no-update", flags.noUpdate},
		{"sw-no-bincache", flags.noBincache},
		{"sw-hash-all-files", flags.hashAllFiles},
		{"sw-allow-unadmitted", flags.allowUnadmitted},
		{"sw-index", flags.index != ""},
		{"target", flags.target != ""},
	} {
		if option.set {
			return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "run: --" + option.name + " requires a retained source, admission or private-file binding before intake"}
		}
	}
	if !flags.detached {
		return refuseDetachedOnlyFlags(flags)
	}
	return nil
}

func refuseDetachedOnlyFlags(flags runFlags) error {
	if flags.idempotencyKey != "" {
		return fmt.Errorf("run: --sw-idempotency-key is read only by a detached launch; add --sw-detached, or drop the flag")
	}
	return nil
}

func validateLocalIntake(response controllerwire.CreateRunResponse, pipeline string) error {
	if err := validateLocalReceipt(response); err != nil {
		return err
	}
	request := response.Request.Request
	if response.Number <= 0 || request.Pipeline != pipeline || request.Origin != runspec.OriginLocal || request.Mode != runspec.IntakeExecute || request.Dispatch != runspec.DispatchHost {
		return localRunProtocolError("controller returned a different local pipeline or intake purpose")
	}
	return nil
}

func validateLocalReceipt(response controllerwire.CreateRunResponse) error {
	sealed := response.Request
	hash, err := runspec.CanonicalHash(sealed)
	if err != nil || response.RunID == "" || sealed.RunID != response.RunID || sealed.Version != 1 || sealed.Request.Version != 1 || sealed.Hash == "" || hash != sealed.Hash || response.HereBearer != "" {
		return localRunProtocolError("controller returned an invalid local intake receipt")
	}
	return nil
}

func hostLocalRun(ctx context.Context, runtime localRunRuntime, run string, requestHash runspec.Hash, version string) (completion nodehost.RunCompletion, resultErr error) {
	if err := ctx.Err(); err != nil {
		return completion, err
	}
	response, err := runtime.controller.Claim(ctx, controllerwire.ClaimRequest{RunID: run, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: version, AvailableCores: float64(runtimehost.NumCPU())})
	if err != nil {
		return completion, err
	}
	if response.Claim == nil {
		return completion, &apierr.Error{Code: apierr.LockHeld, Message: "local run has no available owner claim"}
	}
	award := *response.Claim
	masker := secrets.NewMasker()
	masker.Register(award.Capability)
	for _, value := range award.Request.Request.RequestedArgs {
		masker.Register(value)
	}
	if award.Accepted != nil {
		for _, value := range award.Accepted.Spec.Resolved.Args {
			masker.Register(value)
		}
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	input, err := localRunInput(run, requestHash, award)
	if err != nil {
		return completion, err
	}
	return runOwnerSession(ctx, award, input, func(ctx context.Context, award controllerwire.ClaimedUnit) (hereHostSession, error) {
		session, err := runtime.bind(ctx, award)
		if err != nil {
			return session, err
		}
		session.input.Storage.RemoteCache = nil
		if award.Request.Request.Options.CacheFrom.Value != "" {
			if runtime.remoteCache == nil {
				return session, &apierr.Error{Code: apierr.Unavailable, Message: "local host requires its sealed remote-cache binding"}
			}
			session.input.Storage.RemoteCache = runtime.remoteCache
		}
		return session, nil
	})
}

func localRunInput(run string, requestHash runspec.Hash, award controllerwire.ClaimedUnit) (nodehost.RunInput, error) {
	if requestHash != "" && award.Request.Hash != requestHash {
		return nodehost.RunInput{}, localRunProtocolError("controller returned a different local request seal")
	}
	return ownerRunInput(run, award, capability.PurposeTrigger)
}

func localRunProtocolError(message string) error {
	return &apierr.Error{Code: apierr.ProtocolError, Message: message}
}

func emitLocalRunReceipt(out io.Writer, response controllerwire.CreateRunResponse, format string) error {
	if strings.TrimSpace(response.RunID) == "" {
		return localRunProtocolError("detached submission produced no run ID")
	}
	receipt := struct {
		RunID    string `json:"run_id"`
		Replayed bool   `json:"replayed"`
	}{response.RunID, response.Replayed}
	switch format {
	case "json":
		return json.NewEncoder(out).Encode(receipt)
	case "plain":
		_, err := fmt.Fprintln(out, receipt.RunID)
		return err
	default:
		_, err := fmt.Fprintf(out, "run %s submitted\n", receipt.RunID)
		return err
	}
}

func runLocalInvocation(pipeline string, flags runFlags, args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runLocalCommand(ctx, pipeline, flags, args, os.Stdout, localRunDependencies{resolve: resolveLocalRunRuntime, version: installedVersion()})
}
