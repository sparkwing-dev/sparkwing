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
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/blob"
	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type localRunOptions struct {
	standaloneReason, stateDB, environmentFile string
	standalone                                 bool
}

type localRunRuntime struct {
	controller       controllerwire.Controller
	ensureController func(context.Context) error
	prepare          func(context.Context, string, runFlags) (localRunPreparation, error)
	bind             func(context.Context, controllerwire.ClaimedUnit) (hereHostSession, error)
	close            func() error
	remoteCache      blob.RemoteCache
	openCache        func(context.Context, string, *runspec.Request) (blob.RemoteCache, func() error, error)
	publish          func(context.Context, string, string) error
	follow           func(context.Context, string, string, io.Writer) error
}

type localRunDependencies struct {
	interrupts <-chan os.Signal
	resolve    func(context.Context, localRunOptions) (localRunRuntime, error)
	version    string
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
	runtime, err := deps.resolve(ctx, localRunOptions{standalone: flags.allowUnadmitted})
	if err != nil {
		return err
	}
	if runtime.close != nil {
		defer func() { resultErr = errors.Join(resultErr, runtime.close()) }()
	}
	if runtime.controller == nil || runtime.prepare == nil || runtime.close == nil || (!flags.detached && (runtime.bind == nil || runtime.follow == nil)) {
		return &apierr.Error{Code: apierr.Unavailable, Message: "local run integration has incomplete intake or host bindings"}
	}
	prepared, err := runtime.prepare(ctx, pipeline, flags)
	if err != nil {
		return err
	}
	for _, value := range prepared.localSecrets {
		masker.Register(value)
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
		if runtime.openCache == nil {
			return &apierr.Error{Code: apierr.Unavailable, Message: "run: remote cache requires an authorized read-only storage binding before intake"}
		}
		var closeCache func() error
		runtime.remoteCache, closeCache, err = runtime.openCache(ctx, request.Request.Options.CacheFrom.Value, &request.Request)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, closeCache()) }()
	}
	if runtime.ensureController != nil {
		if err := runtime.ensureController(ctx); err != nil {
			return err
		}
	}
	if mode := strings.TrimSpace(flags.priority); mode == wingwire.PriorityFront || mode == wingwire.PriorityBack {
		queue, ok := runtime.controller.(controllerwire.QueueManager)
		if !ok {
			return &apierr.Error{Code: apierr.Unavailable, Message: "run: controller cannot resolve relative priority"}
		}
		rank, err := resolveRelativeRunPriority(ctx, mode, queue)
		if err != nil {
			return err
		}
		request.Request.Options.Priority.Value = rank
	}
	response, err := runtime.controller.CreateRun(ctx, request)
	masker.Register(response.HereBearer)
	if err != nil {
		return err
	}
	if err := validateLocalIntake(response, pipeline); err != nil {
		return err
	}
	if response.Warning != "" {
		fmt.Fprintln(os.Stderr, "warning: "+response.Warning)
	}
	if handlePath != "" {
		if err := orchestrator.PublishRunHandle(handlePath, orchestrator.NewRunHandle(response.RunID, pipeline, "pending")); err != nil {
			return fmt.Errorf("run %s is admitted but its handle could not be published: %w", response.RunID, err)
		}
	}
	format := request.Request.Options.Output.Value
	if flags.detached {
		return errors.Join(emitLocalRunReceipt(out, response, format), ctx.Err())
	}
	if err := emitLocalRunEvent(out, format, "run_start", response.RunID, pipeline, ""); err != nil {
		return fmt.Errorf("run %s is admitted but its start record could not be written: %w", response.RunID, err)
	}
	host := func(ctx context.Context) (nodehost.RunCompletion, error) {
		return hostLocalRun(ctx, runtime, response.RunID, "", deps.version)
	}
	completion, err := observeHereRun(ctx, response.RunID, hereObservation{host: host, follow: func(ctx context.Context) error {
		return runtime.follow(ctx, response.RunID, request.Request.Options.Output.Value, out)
	}}, nil)
	if err != nil {
		return fmt.Errorf("run %s is admitted: %w", response.RunID, err)
	}
	if profile := request.Request.Options.Publish.Value; profile != "" {
		if runtime.publish == nil {
			_, _ = fmt.Fprintln(os.Stderr, "warning: local run completed but publication is unavailable")
		} else if err := publishLocalCompletion(ctx, deps.interrupts, runtime.publish, profile, response.RunID); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				_, _ = fmt.Fprintln(os.Stderr, "warning: publication abandoned; run stayed local-only")
			} else {
				_, _ = fmt.Fprintln(os.Stderr, "warning: local run completed but publication failed:", masker.Mask(err.Error()))
			}
		}
	}
	outputErr := emitLocalRunEvent(out, format, "run_finish", response.RunID, pipeline, completion.Outcome)
	if outputErr != nil {
		outputErr = fmt.Errorf("run %s is %s but its completion record could not be written: %w", response.RunID, completion.Outcome, outputErr)
	}
	return errors.Join(statusExitCode(completion.Outcome), outputErr)
}

func publishLocalCompletion(parent context.Context, interrupts <-chan os.Signal, publish func(context.Context, string, string) error, profile, run string) error {
	interrupted := parent.Err() != nil
	if interrupts == nil {
		signals := make(chan os.Signal, 2)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		interrupts = signals
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), wingdclient.RequestTimeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if !interrupted {
			select {
			case <-interrupts:
			case <-ctx.Done():
				return
			}
		}
		select {
		case <-interrupts:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer func() { cancel(); <-done }()
	return publish(ctx, profile, run)
}

func validateLocalRunFlags(flags runFlags) error {
	if flags.parseErr != nil {
		return flags.parseErr
	}
	if flags.prioritySet || flags.priority != "" {
		if _, err := validatePriorityFlag(flags.priority); err != nil {
			return err
		}
	}
	if flags.allowUnadmitted && flags.detached {
		return &clisurface.UsageError{Path: "sparkwing run", Message: "--sw-allow-unadmitted uses an in-process controller and cannot detach"}
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
	if response.Inputs.Pipeline != pipeline {
		return localRunProtocolError("controller returned a different local pipeline")
	}
	return nil
}

func validateLocalReceipt(response controllerwire.CreateRunResponse) error {
	if response.RunID == "" || response.Number <= 0 || response.Here || response.HereBearer != "" {
		return localRunProtocolError("controller returned an invalid local intake receipt")
	}
	return nil
}

func hostLocalRun(ctx context.Context, runtime localRunRuntime, run string, requestHash runspec.Hash, version string) (completion nodehost.RunCompletion, resultErr error) {
	if err := ctx.Err(); err != nil {
		return completion, err
	}
	response, err := runtime.controller.Claim(ctx, controllerwire.ClaimRequest{RunID: run, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: version})
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
	request := award.Request.Request
	if request.Origin != runspec.OriginLocal || request.Mode != runspec.IntakeExecute || request.Dispatch != runspec.DispatchHost {
		return nodehost.RunInput{}, localRunProtocolError("controller returned a different local intake purpose")
	}
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
		RunID     string `json:"run_id"`
		RequestID string `json:"request_id,omitempty"`
		Replayed  bool   `json:"replayed"`
		Warning   string `json:"warning,omitempty"`
	}{response.RunID, response.RequestID, response.Replayed, response.Warning}
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

func resolveRelativeRunPriority(ctx context.Context, mode string, queue controllerwire.QueueManager) (int, error) {
	state, err := queue.ReadQueue(ctx)
	if err != nil {
		return 0, err
	}
	rank := wingwire.ResolveRelativePriority(state.Waiters, mode)
	for _, waiter := range state.Waiters {
		if mode == wingwire.PriorityFront && rank <= waiter.Priority || mode == wingwire.PriorityBack && rank >= waiter.Priority {
			return 0, &apierr.Error{Code: apierr.InvalidArgs, Message: "run: relative priority exceeds the integer range"}
		}
	}
	return rank, nil
}
