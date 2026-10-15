package main

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"

	"github.com/sparkwing-dev/sparkwing/internal/capability"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodehost"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	wingdclient "github.com/sparkwing-dev/sparkwing/internal/wingd/client"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/wingwire"
)

type hostedRun struct {
	schema    nodeproto.PipelineDescription
	args      map[string]string
	flags     runFlags
	env       []string
	source    string
	priority  string
	logFormat string
	runHandle string
}

func (r *pipelineRun) host(h hostedRun) error {
	options, closeTransport, err := controllerOptions(r.ctx, "")
	if err != nil {
		return err
	}
	defer closeTransport()
	deps := defaultHereDependencies(h.logFormat)
	dir := r.opts.ExecutionDir
	if dir == "" {
		dir = r.sparkwingDir
	}
	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	snapshots, err := nodehost.NewFileEnvironmentSnapshots(filepath.Join(paths.Root, "captured-environments"))
	if err != nil {
		return err
	}
	h.priority, err = localHostPriority(r.ctx, h.priority, func(ctx context.Context) (wingwire.QueueState, error) {
		return wingdclient.Query(ctx, wingdclient.Options{Version: installedVersion(), NoTakeover: true, Spawn: wingdclient.NoHostSpawn})
	})
	if err != nil {
		return err
	}
	request, err := prepareLocalHostRequest(r.ctx, filepath.Dir(dir), r.pipeline, h, snapshots)
	if err != nil {
		return err
	}
	r.stopSignals()
	return submitLocalHosted(context.WithoutCancel(r.ctx), options, request, h.runHandle, os.Stdout, deps)
}

func localHostPriority(ctx context.Context, raw string, query func(context.Context) (wingwire.QueueState, error)) (string, error) {
	if raw != wingwire.PriorityFront && raw != wingwire.PriorityBack {
		return raw, nil
	}
	queue, err := query(ctx)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(wingwire.ResolveRelativePriority(queue.Waiters, raw)), nil
}

func prepareLocalHostRequest(ctx context.Context, directory string, pipeline nodehost.PipelineIdentity, run hostedRun, snapshots *nodehost.FileEnvironmentSnapshots) (controllerwire.CreateRunRequest, error) {
	if run.flags.startAt != "" || run.flags.stopAt != "" || run.flags.only != "" {
		return controllerwire.CreateRunRequest{}, &apierr.Error{Code: apierr.PolicyMissing, Message: "local hosting requires sealed step selections"}
	}
	repository, err := runspec.CanonicalRepository(runspec.RepoInput{AbsolutePath: directory})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	runner := runspec.RunnerSelection{Kind: runspec.RunnerAny}
	flags := settings.Values{
		RunnerSelection: &runner, DryRun: &run.flags.dryRun, NoMemo: &run.flags.noCache, AllowRisk: run.flags.allow,
		Pause: &settings.PauseSelection{Before: run.flags.pauseBefore, After: run.flags.pauseAfter, OnFailure: run.flags.pauseOnFailure},
	}
	if run.flags.workers != 0 {
		flags.Workers = &run.flags.workers
	}
	if run.logFormat != "" && run.logFormat != "quiet" {
		flags.Output = &run.logFormat
	}
	if run.priority != "" {
		priority, err := strconv.Atoi(run.priority)
		if err != nil {
			return controllerwire.CreateRunRequest{}, &apierr.Error{Code: apierr.InvalidArgs, Message: "local priority must resolve to an integer"}
		}
		flags.Priority = &priority
	}
	options, err := settings.ResolveRequest(settings.Inputs{Flags: flags, Context: settings.Context{Origin: runspec.OriginLocal, Local: true, NumCPU: runtime.NumCPU()}})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	environment, err := snapshots.Capture(ctx, run.env)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	git := orchestrator.DetectGit(directory)
	secretArgs := []string{}
	for _, arg := range run.schema.Args {
		if arg.Secret {
			secretArgs = append(secretArgs, arg.Name)
		}
	}
	return controllerwire.CreateRunRequest{Request: runspec.Request{
		Version: 1, Mode: runspec.IntakeExecute, Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost,
		Pipeline: pipeline.Invoked, RequestedArgs: run.args, SecretArgs: runspec.Optional[[]string]{Set: true, Value: secretArgs},
		Git:     runspec.Git{Repository: repository, SHA: git.SHA, Ref: git.Branch},
		Source:  runspec.Source{Ref: git.SHA, Entrypoint: pipeline.Entrypoint, Location: runspec.SourceLocation{LocalCheckout: directory, PipelineDir: ".sparkwing"}},
		Options: options, Env: environment,
	}}, nil
}

func submitLocalHosted(ctx context.Context, options client.EngineOptions, request controllerwire.CreateRunRequest, handlePath string, out io.Writer, deps hereDependencies) (resultErr error) {
	masks := secrets.NewMasker()
	for _, value := range request.Request.RequestedArgs {
		masks.Register(value)
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masks}
		}
	}()
	runtime, err := deps.resolve(ctx, options)
	if err != nil {
		return err
	}
	if runtime.controller == nil || runtime.bind == nil {
		return &apierr.Error{Code: apierr.PolicyMissing, Message: "local hosting requires a controller and shared host binding"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if handlePath != "" {
		release, err := orchestrator.ReserveRunHandle(handlePath)
		if err != nil {
			return err
		}
		defer release()
	}
	if request.IdempotencyKey == "" {
		request.IdempotencyKey = rand.Text()
	}
	response, err := runtime.controller.CreateRun(ctx, request)
	masks.Register(response.HereBearer)
	if err != nil {
		return err
	}
	receipt, err := initialTriggerReceipt(request.Request.Pipeline, false, response)
	if err != nil {
		return err
	}
	if handlePath != "" {
		if err := orchestrator.PublishRunHandle(handlePath, orchestrator.NewRunHandle(response.RunID, request.Request.Pipeline, "", "pending")); err != nil {
			return err
		}
	}
	award, err := runtime.controller.Claim(ctx, controllerwire.ClaimRequest{RunID: response.RunID, Kinds: []capability.UnitKind{capability.UnitTrigger}, RunnerVersion: installedVersion()})
	if award.Claim != nil {
		masks.Register(award.Claim.Capability)
	}
	if err != nil {
		return err
	}
	if award.Claim == nil {
		return &apierr.Error{Code: apierr.LockHeld, Message: "local run has no available owner claim"}
	}
	if award.Claim.RunID != response.RunID || !reflect.DeepEqual(award.Claim.Request, response.Request) || award.Claim.Purpose != capability.PurposeTrigger {
		return &apierr.Error{Code: apierr.ProtocolError, Message: "local award differs from accepted intake"}
	}
	host := func(ctx context.Context) (nodehost.RunCompletion, error) {
		return hostLocalAward(ctx, options, *award.Claim, runtime)
	}
	var completion nodehost.RunCompletion
	if deps.attach != nil {
		completion, err = deps.attach(ctx, options, response.RunID, host)
	} else {
		completion, err = host(ctx)
	}
	receipt.Outcome = completion.Outcome
	if err == nil && !isTerminalRunStatus(completion.Outcome) {
		err = &apierr.Error{Code: apierr.ProtocolError, Message: "local host returned no terminal outcome"}
	}
	if err != nil {
		if !errors.As(&maskedCLIError{err: err, masker: masks}, &receipt.Error) {
			receipt.Error = &apierr.Error{Code: apierr.Unavailable, Message: masks.Mask(err.Error())}
		}
	}
	format := request.Request.Options.Output.Value
	if format == "" {
		format = "plain"
	}
	return errors.Join(err, statusExitCode(completion.Outcome), writeTriggerReceipt(out, receipt, format))
}
