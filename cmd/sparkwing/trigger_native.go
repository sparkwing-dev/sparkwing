package main

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func prepareNativeTrigger(ctx context.Context, flags triggerFlags) (result controllerwire.CreateRunRequest, resultErr error) {
	unavailable := func(message string) (controllerwire.CreateRunRequest, error) {
		return controllerwire.CreateRunRequest{}, &apierr.Error{Code: apierr.Unavailable, Message: message}
	}
	if len(flags.allowSecretFiles) != 0 && !flags.workingTree {
		return unavailable("--allow-secret-file requires --working-tree")
	}
	path, err := userconfig.Path()
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	machine, err := userconfig.ReadConfig(path)
	if err != nil && !os.IsNotExist(err) {
		return controllerwire.CreateRunRequest{}, err
	}
	var localSecrets []string
	if machine.Run != nil && machine.Run.Routing != nil {
		localSecrets = machine.Run.Routing.LocalSecrets
	}
	environment := prepareLocalEnvironment(os.Environ(), localSecrets)
	captured, masker := environment.captured, environment.masker
	var allow []string
	if machine.Run != nil {
		allow = machine.Run.SubmitEnvAllow
	}
	submitted, err := procenv.Submit(captured, allow, masker)
	if err != nil {
		return result, err
	}
	defer func() {
		if resultErr != nil {
			resultErr = &maskedCLIError{err: resultErr, masker: masker}
		}
	}()
	checkout, err := os.Getwd()
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	var snapshot *worktreeSnapshot
	originalCheckout := checkout
	if flags.workingTree {
		snapshot, err = captureWorktreeSnapshot(ctx, captured, checkout, flags.allowSecretFiles)
		if err != nil {
			return result, err
		}
		defer func() { resultErr = errors.Join(resultErr, snapshot.close()) }()
	}
	selected, err := prepareTriggerSource(ctx, triggerSourceInput{checkout: originalCheckout, snapshot: snapshot, pipeline: flags.pipeline, ref: flags.ref, pipelineRef: flags.pipelineRef, environment: captured, githubID: func(ctx context.Context, remote string) (int64, error) {
		return lookupTriggerGitHubID(ctx, remote, captured)
	}})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if err := selected.checkProfile(flags.profile); err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if snapshot == nil {
		if err := selected.checkReachable(ctx); err != nil {
			return result, err
		}
	} else {
		branch, branchErr := (triggerCheckout{directory: originalCheckout, environment: selected.checkout.environment}).read(ctx, "symbolic-ref", "--quiet", "--short", "HEAD")
		if branchErr != nil && !gitExitCode(branchErr, 1) {
			return result, branchErr
		}
		selected.git.Ref = branch
		if err := snapshot.includeRevisions(ctx, selected.source.Location.RemoteURL, selected.git.SHA, selected.source.Ref); err != nil {
			return result, err
		}
	}
	home, err := wingd.HomeDir("")
	if err != nil {
		return result, err
	}
	options, err := hostSourceOptions(home, captured, masker, false)
	if err != nil {
		return result, err
	}
	options.SDK = ""
	if snapshot != nil {
		options.LocalBundle = snapshot.BundlePath
	}

	described, err := inspectTriggerSource(ctx, selected, captured, options)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	input := settings.Inputs{Context: settings.Context{Origin: runspec.OriginTrigger, Submitter: true, NumCPU: runtime.NumCPU()}, Flags: settings.Values{Options: runspec.Options{Profile: runspec.Setting[string]{Optional: runspec.Optional[string]{Set: true, Value: flags.profile}}}}}
	input.Machine.SubmitEnvAllow.Set, input.Machine.SubmitEnvAllow.Value = allow != nil, allow
	input.Machine.SubmitEnvAllow.Origin = runspec.SettingOrigin{Layer: runspec.LayerMachine, File: path, Key: "run.submit_env_allow"}
	if flags.runner != "" {
		selection := runspec.RunnerSelection{Kind: runspec.RunnerName, Value: flags.runner}
		if flags.runner == hereRunnerFlag {
			selection.Kind, selection.Value = runspec.RunnerHere, ""
		}
		if label, ok := strings.CutPrefix(flags.runner, "label="); ok {
			selection.Kind, selection.Value = runspec.RunnerLabel, label
		}
		input.Flags.Runner.Set, input.Flags.Runner.Value = true, selection
	}
	result, err = prepareTriggerRequest(triggerRequestInput{source: selected, settings: input, described: described, reason: flags.reason, args: flags.passthrough, submittedEnv: submitted})
	if err != nil || snapshot == nil {
		return result, err
	}
	connection, closeConnection, err := controllerOptions(ctx, flags.profile)
	if err != nil {
		return result, err
	}
	defer closeConnection()
	digest, err := client.UploadSourceSnapshot(ctx, connection, source.Publication{Git: result.Request.Git, Source: result.Request.Source, Path: snapshot.BundlePath, Base: procenv.Base{Local: true, Captured: captured, Masker: masker}, Secrets: environment.held})
	if err != nil {
		return result, err
	}
	result.Request.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	return result, nil
}
