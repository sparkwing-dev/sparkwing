package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/nodeproto"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

type localRunPreparation struct {
	identity    clisurface.PipelineIdentity
	described   []nodeproto.PipelineDescription
	git         runspec.Git
	source      runspec.Source
	environment runspec.EnvSnapshot
	machine     userconfig.Config
	machineFile string
	settings    settings.Inputs
}

func prepareLocalRunRequest(ctx context.Context, input localRunPreparation, flags runFlags, args []string) (_ controllerwire.CreateRunRequest, resultErr error) {
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
	if err := ctx.Err(); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := validateLocalRunFlags(flags); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := userconfig.ValidateConfig(input.machine); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if input.identity.Name == "" || input.identity.Entrypoint == "" || input.source.Entrypoint != input.identity.Entrypoint || input.environment.Digest == "" || input.settings.Context.NumCPU <= 0 {
		return controllerwire.CreateRunRequest{}, errors.New("local preparation requires captured pipeline, source, environment and machine facts")
	}
	schema, err := boundPipelineSchema(input.described, input.identity)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	requested, err := parsePipelineArgs(schema, args)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	inputs := input.settings
	inputs.Context.Origin, inputs.Context.Submitter, inputs.Context.Local = runspec.OriginLocal, true, true
	inputs.Context.Detached = flags.detached
	inputs.Flags = localRunSettingFlags(flags)
	if input.machine.Run != nil {
		inputs.Machine.CacheFrom = input.machine.Run.CacheFrom
		inputs.Machine.SubmitEnvAllow = input.machine.Run.SubmitEnvAllow
	}
	if input.machine.Debug != nil && input.machine.Debug.PauseTimeout != nil {
		duration, err := time.ParseDuration(*input.machine.Debug.PauseTimeout)
		if err != nil {
			return controllerwire.CreateRunRequest{}, fmt.Errorf("debug.pause_timeout: expected a positive duration")
		}
		inputs.Machine.PauseTimeout = &duration
	}
	inputs.Machine.Origins = maps.Clone(inputs.Machine.Origins)
	if inputs.Machine.Origins == nil {
		inputs.Machine.Origins = map[string]settings.Origin{}
	}
	for _, key := range []string{"cache_from", "run.submit_env_allow", "debug.pause_timeout"} {
		inputs.Machine.Origins[key] = settings.Origin{Source: settings.Machine, File: input.machineFile, Key: key}
	}
	options, err := settings.ResolveRequest(inputs)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := enforceRiskGate(input.identity.Name, risksIn(schema), runFlags{allow: options.AllowRisk.Value, dryRun: options.DryRun.Value}); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	secretArgs := []string{}
	for _, argument := range schema.Args {
		if argument.Secret {
			secretArgs = append(secretArgs, argument.Name)
		}
	}
	return controllerwire.CreateRunRequest{IdempotencyKey: flags.idempotencyKey, Request: runspec.Request{
		Version: 1, Pipeline: input.identity.Name, Mode: runspec.IntakeExecute, Origin: runspec.OriginLocal, Dispatch: runspec.DispatchHost,
		RequestedArgs: requested, SecretArgs: runspec.Optional[[]string]{Set: true, Value: secretArgs},
		Git: input.git, Source: input.source, Env: input.environment, Options: options,
	}}, nil
}

func localRunSettingFlags(flags runFlags) settings.Values {
	has := func(name string, fallback bool) bool {
		if flags.invocation.Command.Path != "" {
			return flags.invocation.Has(name)
		}
		return fallback
	}
	values := settings.Values{}
	if has("publish", flags.publish != "") {
		values.Publish = &flags.publish
	}
	if has("sw-cache-from", flags.cacheFrom != "") {
		values.CacheFrom = &flags.cacheFrom
	}
	if has("sw-workers", flags.workers != 0) {
		values.Workers = &flags.workers
	}
	if has("sw-no-memo", flags.noCache) {
		values.NoMemo = &flags.noCache
	}
	if has("sw-dry-run", flags.dryRun) {
		values.DryRun = &flags.dryRun
	}
	if has("sw-output", flags.outputFormat != "") {
		values.Output = &flags.outputFormat
	}
	if has("sw-priority", flags.prioritySet || flags.priority != "") {
		priority, _ := strconv.Atoi(flags.priority)
		values.Priority = &priority
	}
	if has("sw-allow-risk", flags.allow != nil) {
		values.AllowRisk = flags.allow
	}
	pause := settings.PauseSelection{}
	if has("sw-pause-before", flags.pauseBefore != nil) {
		pause.Before = flags.pauseBefore
	}
	if has("sw-pause-after", flags.pauseAfter != nil) {
		pause.After = flags.pauseAfter
	}
	if has("sw-pause-on-failure", flags.pauseOnFailure != nil) {
		pause.OnFailure = flags.pauseOnFailure
	}
	if pause.Before != nil || pause.After != nil || pause.OnFailure != nil {
		values.Pause = &pause
	}
	return values
}
