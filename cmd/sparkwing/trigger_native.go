package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/engine"
	"github.com/sparkwing-dev/sparkwing/internal/procenv"
	"github.com/sparkwing-dev/sparkwing/internal/releaseasset"
	"github.com/sparkwing-dev/sparkwing/internal/runspec"
	"github.com/sparkwing-dev/sparkwing/internal/settings"
	"github.com/sparkwing-dev/sparkwing/internal/source"
	"github.com/sparkwing-dev/sparkwing/internal/toolchain"
	"github.com/sparkwing-dev/sparkwing/internal/userconfig"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func prepareNativeTrigger(ctx context.Context, flags triggerFlags) (result controllerwire.CreateRunRequest, resultErr error) {
	unavailable := func(message string) (controllerwire.CreateRunRequest, error) {
		return controllerwire.CreateRunRequest{}, &apierr.Error{Code: apierr.Unavailable, Message: message}
	}
	if flags.runner == hereRunnerFlag {
		return unavailable("here intake requires the reason to survive the sealed request and replay")
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
	if machine.Run != nil && len(machine.Run.SubmitEnvAllow) != 0 {
		return unavailable("trigger environment submission requires private captured-value retention at the controller")
	}
	captured := os.Environ()
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
		if selected.source.Ref != snapshot.SHA {
			if err := snapshot.includeRevision(ctx, selected.source.Ref); err != nil {
				return result, err
			}
		}
		if _, err := selected.checkout.read(ctx, "checkout", "--quiet", "--detach", selected.source.Ref, "--"); err != nil {
			return result, err
		}
	}
	sdk, err := selected.sdk(ctx)
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	mode, err := toolchain.ParseMode(os.Getenv(toolchainModeEnv))
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	engines, err := engine.NewResolver(engine.Config{BuildVersion: installedVersion(), Mode: mode})
	if err != nil {
		return controllerwire.CreateRunRequest{}, err
	}
	if snapshot == nil {
		workspace, err := os.MkdirTemp("", "sparkwing-trigger-inspect-*")
		if err != nil {
			return result, err
		}
		defer func() { resultErr = errors.Join(resultErr, os.RemoveAll(workspace)) }()
		checkout, err = triggerInspectionCheckout(ctx, selected.checkout.directory, selected.source.Ref, "", workspace)
		if err != nil {
			return result, err
		}
	}
	options := source.Options{SDK: sdk, Engines: engines, PreparedCheckout: checkout, Target: releaseasset.Target{Binary: releaseasset.Sparkwing, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}}
	described, err := inspectTriggerSource(ctx, selected, captured, triggerInspection{options: options})
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
	result, err = prepareTriggerRequest(triggerRequestInput{source: selected, settings: input, described: described, args: flags.passthrough, env: map[string]string{}})
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
	environment, err := procenv.Build(procenv.Git, procenv.Base{Local: true, Captured: os.Environ()}, procenv.Runtime{})
	if err != nil {
		return "", err
	}
	read := func(directory string, args ...string) (string, error) {
		return (triggerCheckout{directory: directory, environment: environment}).read(ctx, args...)
	}
	directory := filepath.Join(workspace, "checkout")
	if _, err := read(repo, "clone", "--quiet", "--shared", "--no-checkout", "--", repo, directory); err != nil {
		return "", err
	}
	remote, err := read(repo, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	if _, err := read(directory, "remote", "set-url", "origin", strings.TrimSpace(remote)); err != nil {
		return "", err
	}
	if bundle != "" {
		if _, err := read(directory, "bundle", "unbundle", bundle); err != nil {
			return "", err
		}
	}
	if _, err := read(directory, "checkout", "--quiet", "--detach", sha, "--"); err != nil {
		return "", err
	}
	return directory, nil
}
