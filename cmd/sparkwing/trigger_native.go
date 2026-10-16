package main

import (
	"context"
	"os"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
)

func prepareNativeTrigger(ctx context.Context, flags triggerFlags) (controllerwire.CreateRunRequest, error) {
	unavailable := func(message string) (controllerwire.CreateRunRequest, error) {
		return controllerwire.CreateRunRequest{}, &apierr.Error{Code: apierr.Unavailable, Message: message}
	}
	if flags.workingTree {
		return unavailable("source-bundle intake requires an authorized shared-storage binding")
	}
	if flags.runner == hereRunnerFlag {
		return unavailable("here intake requires the reason to survive the sealed request and replay")
	}
	if len(flags.allowSecretFiles) != 0 {
		return unavailable("source-bundle secret-file approval requires an authorized shared-storage binding")
	}
	path, err := userconfig.Path()
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	machine, err := userconfig.ReadConfig(path)
	if err != nil && !os.IsNotExist(err) {
		return controllerwire.CreateRunRequest{}, err
	}
	if machine.Run != nil && len(machine.Run.SubmitEnvAllow) != 0 {
		return unavailable("trigger environment submission requires private captured-value retention at the controller")
	}
	captured := os.Environ()
	checkout, err := os.Getwd()
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	selected, err := prepareTriggerSource(ctx, triggerSourceInput{checkout: checkout, pipeline: flags.pipeline, ref: flags.ref, pipelineRef: flags.pipelineRef, environment: captured})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := selected.checkProfile(flags.profile); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := selected.checkReachable(ctx); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	sdk, err := selected.sdk(ctx)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	mode, err := toolchain.ParseMode(os.Getenv(toolchainModeEnv))
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	engines, err := engine.NewResolver(engine.Config{Mode: mode})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	described, err := inspectTriggerSource(ctx, selected, captured, triggerInspection{options: source.Options{SDK: sdk, Engines: engines, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	input := settings.Inputs{Context: settings.Context{Origin: runspec.OriginTrigger, Submitter: true, NumCPU: runtime.NumCPU()}, Flags: settings.Values{Options: runspec.Options{Profile: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: flags.profile}}}}}
	if flags.runner != "" {
		selection := runspec.RunnerSelection{Kind: runspec.RunnerName, Value: flags.runner}
		if label, ok := strings.CutPrefix(flags.runner, "label="); ok {
			selection.Kind, selection.Value = runspec.RunnerLabel, label
		}
		input.Flags.Runner.Set, input.Flags.Runner.Value = true, selection
	}
	return prepareTriggerRequest(triggerRequestInput{source: selected, settings: input, described: described, args: flags.passthrough, env: map[string]string{}})
}
