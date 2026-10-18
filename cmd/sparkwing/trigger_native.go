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
	"github.com/sparkwing-dev/sparkwing/internal/secrets"
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
	captured := os.Environ()
	masker := secrets.NewMasker()
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
		snapshot, err = captureWorktreeSnapshot(ctx, checkout, flags.allowSecretFiles)
		if err != nil {
			return result, err
		}
		defer func() { resultErr = errors.Join(resultErr, snapshot.close()) }()
		checkout, err = snapshotTriggerCheckout(ctx, snapshot)
		if err != nil {
			return result, err
		}
	}
	selected, err := prepareTriggerSource(ctx, triggerSourceInput{checkout: checkout, pipeline: flags.pipeline, ref: flags.ref, pipelineRef: flags.pipelineRef, environment: captured, githubID: func(ctx context.Context, remote string) (int64, error) {
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
		if err := snapshot.includeRevisions(ctx, selected.git.SHA, selected.source.Ref); err != nil {
			return result, err
		}
		checkout, err = triggerInspectionCheckout(ctx, selected.checkout.directory, selected.git.SHA, snapshot.BundlePath, snapshot.tempDir)
		if err != nil {
			return result, err
		}
		selected.checkout.directory = checkout
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
	if snapshot == nil {
		workspace, err := os.MkdirTemp("", "sparkwing-trigger-inspect-*")
		if err != nil {
			return result, err
		}
		defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(workspace)) }()
		checkout, err = triggerInspectionCheckout(ctx, selected.checkout.directory, selected.git.SHA, "", workspace)
		if err != nil {
			return result, err
		}
	}
	options.PreparedCheckout = checkout

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
	digest, err := client.UploadSourceSnapshot(ctx, connection, result.Request.Git, result.Request.Source, snapshot.BundlePath)
	if err != nil {
		return result, err
	}
	result.Request.Source.BundleDigest = runspec.Optional[string]{Set: true, Value: digest}
	return result, nil
}

func snapshotTriggerCheckout(ctx context.Context, snapshot *worktreeSnapshot) (string, error) {
	return triggerInspectionCheckout(ctx, snapshot.RepoRoot, snapshot.SHA, snapshot.BundlePath, snapshot.tempDir)
}

func triggerInspectionCheckout(ctx context.Context, repo, sha, bundle, workspace string) (string, error) {
	options := source.Options{Workspace: workspace, Base: procenv.Base{Local: true, Captured: os.Environ(), Masker: secrets.NewMasker()}}
	options.LocalReplica, options.LocalBundle = true, bundle
	entry, err := source.PrepareCheckout(ctx, source.Spec{RepoKey: "inspection", LocalCheckout: repo, SHA: sha, CommittedCheckout: true, PipelineDir: "."}, options)
	if err != nil {
		return "", err
	}
	environment, err := procenv.Build(procenv.Git, options.Base, procenv.Runtime{})
	if err != nil {
		return "", err
	}
	reader := triggerCheckout{directory: entry.CheckoutDirectory(), environment: environment}
	remote, err := (triggerCheckout{directory: repo, environment: environment}).read(ctx, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	_, err = reader.read(ctx, "remote", "set-url", "origin", remote)
	return reader.directory, err
}
