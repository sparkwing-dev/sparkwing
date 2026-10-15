package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

type triggerFlags struct {
	pipeline                         string
	profile                          string
	detach                           bool
	workingTree                      bool
	wantHelp                         bool
	allowSecretFiles                 []string
	runner                           string
	reason                           string
	passthrough                      []string
	resume, ref, pipelineRef, output string
}

func parseTriggerFlags(args []string) (triggerFlags, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "trigger"}, args...))
	if err != nil {
		return triggerFlags{}, err
	}
	flags := triggerFlags{
		pipeline: inv.Pipeline, profile: inv.Value("profile"), detach: inv.Bool("detach"),
		workingTree: inv.Bool("working-tree"), wantHelp: inv.Help(),
		allowSecretFiles: inv.Flags["allow-secret-file"], runner: inv.Value("runner"),
		reason: inv.Value("reason"), passthrough: inv.Args, resume: inv.Value("resume"),
		ref: inv.Value("ref"), pipelineRef: inv.Value("pipeline-ref"), output: inv.Output(color.IsInteractiveStdout()),
	}
	if !flags.wantHelp && flags.resume == "" {
		if err := checkHereFlags(flags); err != nil {
			return triggerFlags{}, &clisurface.UsageError{Path: inv.Command.Path, Message: err.Error()}
		}
	}
	return flags, nil
}

func runPipelineTrigger(args []string) error {
	flags, err := parseTriggerFlags(args)
	if flags.wantHelp {
		PrintHelp(cmdPipelineTrigger, os.Stdout)
		return nil
	}
	if err != nil {
		PrintHelp(cmdPipelineTrigger, os.Stderr)
		return fmt.Errorf("pipeline trigger: %w", err)
	}
	if flags.resume != "" || flags.pipelineRef != "" {
		return &apierr.Error{Code: apierr.CapabilityUnsupported, Message: "pipeline trigger: resume and pipeline revision selection are unavailable in this build"}
	}
	if flags.profile == "" {
		return exitErrorf(2, "pipeline trigger: --profile NAME is required (the controller this trigger submits to)")
	}

	if err := refuseOtherThanPinnedProfile(flags.pipeline, flags.profile, flags.workingTree); err != nil {
		return err
	}
	prof, err := resolveProfileFlag(flags.profile)
	if err != nil {
		return err
	}
	if prof.ControllerURL() == "" {
		return fmt.Errorf("pipeline trigger: profile %q has no controller; `pipeline trigger` requires a profile that defines a controller", prof.Name)
	}

	source := triggerSource("pipeline-trigger")
	if flags.workingTree {
		source = triggerSource("pipeline-working-tree")
	}
	if flags.runner == hereRunnerFlag {
		if err := refuseUncommittedCheckout(); err != nil {
			return err
		}
	}
	resp, err := createRemoteTrigger(prof, flags.pipeline, source,
		runFlags{allowSecretFiles: flags.allowSecretFiles, hereReason: flags.reason, runner: flags.runner, ref: flags.ref}, flags.passthrough, flags.workingTree)
	if err != nil {
		return err
	}
	if flags.runner == hereRunnerFlag {
		return hostAndFollowHere(prof, flags.pipeline, resp)
	}

	if flags.detach {
		if flags.output == "json" {
			return jsonEncode(os.Stdout, map[string]string{"run_id": resp.RunID})
		}
		fmt.Fprintln(os.Stdout, resp.RunID)
		return nil
	}

	ctx := context.Background()
	fmt.Fprintf(os.Stderr, "triggered %s on %s as %s (status=%s); following...\n",
		flags.pipeline, prof.Name, resp.RunID, resp.Status)

	format, ferr := resolveTTYAwareOutput(flags.output, "pipeline trigger")
	if ferr != nil {
		return ferr
	}
	followErr := followControllerRunLogs(ctx, client.EngineOptions{
		URL:    prof.ControllerURL(),
		Bearer: func(context.Context) (string, error) { return prof.ControllerToken(), nil },
	}, resp.RunID, format, os.Stdout)
	return remoteFollowExit(ctx, prof, resp.RunID, followErr)
}

func remoteFollowExit(ctx context.Context, prof *namedProfile, runID string, followErr error) error {
	if followErr != nil {
		fmt.Fprintf(os.Stderr, "note: follow of %s ended early (%v); reading the run's status\n", runID, followErr)
	}
	status, fetchErr := orchestrator.RemoteRunOutcome(ctx, prof.ControllerURL(), prof.ControllerToken(), runID, os.Stderr)
	return followExitResult(prof.Name, runID, status, fetchErr, followErr)
}

func followExitResult(profileName, runID, status string, fetchErr, followErr error) error {
	if fetchErr != nil || !isTerminalRunStatus(status) {
		return exitError(3, unknownOutcomeError(profileName, runID, status, fetchErr, followErr))
	}

	if err := statusExitCode(status); err != nil {
		return exitError(exitCodeFor(err), fmt.Errorf("pipeline trigger: run %s: %s", runID, status))
	}
	return nil
}

func unknownOutcomeError(profileName, runID, status string, fetchErr, followErr error) error {
	cause := "last seen " + statusOrUnknown(status)
	if fetchErr != nil {
		cause = fmt.Sprintf("status read failed: %v", fetchErr)
	}
	if followErr != nil {
		cause += fmt.Sprintf("; follow ended: %v", followErr)
	}
	return fmt.Errorf("pipeline trigger: run %s: outcome unknown (%s) -- the run may still be in progress; "+
		"check it with `sparkwing runs status --run %s --profile %s`", runID, cause, runID, profileName)
}

func statusOrUnknown(status string) string {
	if status == "" {
		return "unknown"
	}
	return status
}

// safety: the pin guards against sending a pipeline to the wrong controller by
// mistake; the controller still decides who may run it.
func refuseOtherThanPinnedProfile(pipelineName, profileName string, workingTree bool) error {
	// safety: the runner compiles the .sparkwing/ at the root of the checkout
	// the trigger submits, so that file holds the pin, never a nested one.
	top, err := gitOutput(context.Background(), ".", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("pipeline trigger: find the git checkout to read pipeline %s's pin: %w", pipelineName, err)
	}
	// safety: without --working-tree the controller runs the commit HEAD names,
	// so that commit's sparkwing.yaml holds the pin, not the working tree's.
	ref := "HEAD"
	if workingTree {
		ref = ""
	}
	entry, err := pinnedPipelineAt(filepath.Join(strings.TrimSpace(top), ".sparkwing"), ref, pipelineName)
	if err != nil {
		return fmt.Errorf("pipeline trigger: %w", err)
	}
	if entry == nil || entry.Profile == "" || entry.Profile == profileName {
		return nil
	}
	return fmt.Errorf("pipeline trigger: pipeline %s is pinned to profile %s; pass --profile %s", pipelineName, entry.Profile, entry.Profile)
}
