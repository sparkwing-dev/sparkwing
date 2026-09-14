package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/profile"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type triggerFlags struct {
	pipeline         string
	profile          string
	detach           bool
	workingTree      bool
	wantHelp         bool
	allowSecretFiles []string
	passthrough      []string
}

func parseTriggerFlags(args []string) (triggerFlags, error) {
	if len(args) == 0 {
		return triggerFlags{}, errors.New("pipeline name required (e.g. `sparkwing pipeline trigger release --profile prod`)")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return triggerFlags{wantHelp: true}, nil
	}
	flags := triggerFlags{pipeline: args[0]}
	if strings.HasPrefix(flags.pipeline, "-") {
		return triggerFlags{}, fmt.Errorf("pipeline name must come first; got flag %q", flags.pipeline)
	}

	rest := args[1:]
	i := 0
	for i < len(rest) {
		a := rest[i]
		switch {
		case a == "--":
			flags.passthrough = append(flags.passthrough, rest[i+1:]...)
			i = len(rest)
		case a == "-h" || a == "--help":
			return triggerFlags{wantHelp: true}, nil
		case a == "--profile":
			if i+1 < len(rest) {
				flags.profile = rest[i+1]
				i += 2
				continue
			}
			i++
		case strings.HasPrefix(a, "--profile="):
			flags.profile = strings.TrimPrefix(a, "--profile=")
			i++
		case a == "--detach":
			flags.detach = true
			i++
		case a == "--detach=true":
			flags.detach = true
			i++
		case a == "--detach=false":
			flags.detach = false
			i++
		case a == "--working-tree" || a == "--working-tree=true":
			flags.workingTree = true
			i++
		case a == "--working-tree=false":
			flags.workingTree = false
			i++
		case a == "--allow-secret-file":
			if i+1 < len(rest) {
				flags.allowSecretFiles = append(flags.allowSecretFiles, rest[i+1])
				i += 2
				continue
			}
			i++
		case strings.HasPrefix(a, "--allow-secret-file="):
			flags.allowSecretFiles = append(flags.allowSecretFiles, strings.TrimPrefix(a, "--allow-secret-file="))
			i++
		default:
			flags.passthrough = append(flags.passthrough, a)
			i++
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
	if flags.profile == "" {
		return exitErrorf(2, "pipeline trigger: --profile NAME is required (the controller this trigger submits to)")
	}

	prof, err := resolveProfileFlag(flags.profile)
	if err != nil {
		return err
	}
	if prof.ControllerURL() == "" {
		return fmt.Errorf("pipeline trigger: profile %q has no controller; `pipeline trigger` requires a profile that defines controller:. "+
			"Use sparkwing run --profile %s for local execution against this profile's storage instead", prof.Name, prof.Name)
	}

	source := triggerSource("pipeline-trigger")
	if flags.workingTree {
		source = triggerSource("pipeline-working-tree")
	}
	resp, err := createRemoteTrigger(prof, flags.pipeline, source, runFlags{allowSecretFiles: flags.allowSecretFiles}, flags.passthrough, flags.workingTree)
	if err != nil {
		return err
	}

	if flags.detach {
		fmt.Fprintln(os.Stdout, resp.RunID)
		return nil
	}

	ctx := context.Background()
	fmt.Fprintf(os.Stderr, "triggered %s on %s as %s (status=%s); following...\n",
		flags.pipeline, prof.Name, resp.RunID, resp.Status)

	if prof.Logs != nil {
		format, ferr := resolveTTYAwareOutput("", "pipeline trigger")
		if ferr != nil {
			return ferr
		}
		followErr := orchestrator.JobLogsRemoteWithTokens(ctx, prof.ControllerURL(), prof.ControllerURL(), prof.ControllerToken(),
			resp.RunID, orchestrator.LogsOpts{Follow: true, Format: format, JSON: format == "json"}, os.Stdout)
		return remoteFollowExit(ctx, prof, resp.RunID, followErr)
	}

	fmt.Fprintln(os.Stderr, color.Dim(fmt.Sprintf(
		"note: profile %q declares no logs: backend; following node status (no log bodies). "+
			"Add a logs: spec in profiles.yaml to see streaming output.", prof.Name)))
	followErr := orchestrator.JobStatusRemote(ctx, prof.ControllerURL(), prof.ControllerToken(),
		resp.RunID, orchestrator.StatusOpts{Follow: true}, os.Stdout)
	return remoteFollowExit(ctx, prof, resp.RunID, followErr)
}

func remoteFollowExit(ctx context.Context, prof *profile.Profile, runID string, followErr error) error {
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
