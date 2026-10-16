package main

import (
	"context"
	"errors"
	"fmt"
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
	if flags.outputOrigin.Source == settings.Default {
		inputs.Context.Terminal = flags.outputFormat == "pretty"
	}
	inputs.Flags = localRunSettingFlags(flags)
	if flags.outputOrigin.Source == settings.Env {
		inputs.Environment.Output.Set, inputs.Environment.Output.Value = true, flags.outputFormat
		inputs.Environment.Output.Origin = runspec.SettingOrigin{Layer: runspec.LayerEnv, File: flags.outputOrigin.File, Key: flags.outputOrigin.Key, Line: flags.outputOrigin.Line}
	}
	if input.machine.Run != nil {
		inputs.Machine.CacheFrom = runspec.Setting[string]{}
		if input.machine.Run.CacheFrom != nil {
			inputs.Machine.CacheFrom.Set, inputs.Machine.CacheFrom.Value = true, *input.machine.Run.CacheFrom
			inputs.Machine.CacheFrom.Origin = runspec.SettingOrigin{Layer: runspec.LayerMachine, File: input.machineFile, Key: "cache_from"}
		}
		inputs.Machine.SubmitEnvAllow.Set, inputs.Machine.SubmitEnvAllow.Value = input.machine.Run.SubmitEnvAllow != nil, input.machine.Run.SubmitEnvAllow
		inputs.Machine.SubmitEnvAllow.Origin = runspec.SettingOrigin{Layer: runspec.LayerMachine, File: input.machineFile, Key: "run.submit_env_allow"}
	}
	if input.machine.Debug != nil && input.machine.Debug.PauseTimeout != nil {
		duration, err := time.ParseDuration(*input.machine.Debug.PauseTimeout)
		if err != nil {
			return controllerwire.CreateRunRequest{}, fmt.Errorf("debug.pause_timeout: expected a positive duration")
		}
		inputs.Machine.PauseTimeout.Set, inputs.Machine.PauseTimeout.Value = true, duration
		inputs.Machine.PauseTimeout.Origin = runspec.SettingOrigin{Layer: runspec.LayerMachine, File: input.machineFile, Key: "debug.pause_timeout"}
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
	if has("sw-only", flags.only != "") {
		values.Only.Set, values.Only.Value = true, flags.only
	}
	if has("sw-stop-at", flags.stopAt != "") {
		values.StopAt.Set, values.StopAt.Value = true, flags.stopAt
	}
	if has("sw-start-at", flags.startAt != "") {
		values.StartAt.Set, values.StartAt.Value = true, flags.startAt
	}
	if has("publish", flags.publish != "") {
		values.Publish.Set, values.Publish.Value = true, flags.publish
	}
	if has("sw-cache-from", flags.cacheFrom != "") {
		values.CacheFrom.Set, values.CacheFrom.Value = true, flags.cacheFrom
	}
	if has("sw-workers", flags.workers != 0) {
		values.Workers.Set, values.Workers.Value = true, flags.workers
	}
	if has("sw-no-memo", flags.noCache) {
		values.NoMemo.Set, values.NoMemo.Value = true, flags.noCache
	}
	if has("sw-dry-run", flags.dryRun) {
		values.DryRun.Set, values.DryRun.Value = true, flags.dryRun
	}
	if flags.outputOrigin.Source == settings.Flag || flags.invocation.Command.Path == "" && flags.outputFormat != "" {
		values.Output.Set, values.Output.Value = true, flags.outputFormat
		values.Output.Origin = runspec.SettingOrigin{Layer: runspec.Layer(flags.outputOrigin.Source), File: flags.outputOrigin.File, Key: flags.outputOrigin.Key, Line: flags.outputOrigin.Line}
	}
	if has("sw-priority", flags.prioritySet || flags.priority != "") {
		priority, _ := strconv.Atoi(flags.priority)
		values.Priority.Set, values.Priority.Value = true, priority
	}
	if has("sw-allow-risk", flags.allow != nil) {
		values.AllowRisk.Set, values.AllowRisk.Value = true, flags.allow
	}
	pause := runspec.PauseSelection{}
	if has("sw-pause-before", flags.pauseBefore != nil) {
		pause.Before.Set, pause.Before.Value = true, flags.pauseBefore
	}
	if has("sw-pause-after", flags.pauseAfter != nil) {
		pause.After.Set, pause.After.Value = true, flags.pauseAfter
	}
	if has("sw-pause-on-failure", flags.pauseOnFailure != nil) && flags.pauseOnFailure != nil {
		pause.OnFailure.Set, pause.OnFailure.Value = true, *flags.pauseOnFailure
	}
	if pause.Before.Set || pause.After.Set || pause.OnFailure.Set {
		values.Pause.Set, values.Pause.Value = true, pause
	}
	return values
}
