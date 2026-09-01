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

func parseTriggerFlags(args []string) (pipelineName, profileName string, detach, workingTree, wantHelp bool, passthrough []string, err error) {
	if len(args) == 0 {
		return "", "", false, false, false, nil, errors.New("pipeline name required (e.g. `sparkwing pipeline trigger release --profile prod`)")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return "", "", false, false, true, nil, nil
	}
	pipelineName = args[0]
	if strings.HasPrefix(pipelineName, "-") {
		return "", "", false, false, false, nil, fmt.Errorf("pipeline name must come first; got flag %q", pipelineName)
	}

	rest := args[1:]
	i := 0
	for i < len(rest) {
		a := rest[i]
		switch {
		case a == "--":
			passthrough = append(passthrough, rest[i+1:]...)
			i = len(rest)
		case a == "-h" || a == "--help":
			return "", "", false, false, true, nil, nil
		case a == "--profile":
			if i+1 < len(rest) {
				profileName = rest[i+1]
				i += 2
				continue
			}
			i++
		case strings.HasPrefix(a, "--profile="):
			profileName = strings.TrimPrefix(a, "--profile=")
			i++
		case a == "--detach":
			detach = true
			i++
		case a == "--detach=true":
			detach = true
			i++
		case a == "--detach=false":
			detach = false
			i++
		case a == "--working-tree" || a == "--working-tree=true":
			workingTree = true
			i++
		case a == "--working-tree=false":
			workingTree = false
			i++
		default:
			passthrough = append(passthrough, a)
			i++
		}
	}
	return pipelineName, profileName, detach, workingTree, false, passthrough, nil
}

func runPipelineTrigger(args []string) error {
	pipelineName, profileName, detach, workingTree, wantHelp, passthrough, err := parseTriggerFlags(args)
	if wantHelp {
		PrintHelp(cmdPipelineTrigger, os.Stdout)
		return nil
	}
	if err != nil {
		PrintHelp(cmdPipelineTrigger, os.Stderr)
		return fmt.Errorf("pipeline trigger: %w", err)
	}
	if profileName == "" {
		return exitErrorf(2, "pipeline trigger: --profile NAME is required (the controller this trigger submits to)")
	}

	prof, err := resolveProfileFlag(profileName)
	if err != nil {
		return err
	}
	if prof.ControllerURL() == "" {
		return fmt.Errorf("pipeline trigger: profile %q has no controller; `pipeline trigger` requires a profile that defines controller:. "+
			"Use sparkwing run --profile %s for local execution against this profile's storage instead", prof.Name, prof.Name)
	}

	source := triggerSource("pipeline-trigger")
	if workingTree {
		source = triggerSource("pipeline-working-tree")
	}
	resp, err := createRemoteTrigger(prof, pipelineName, source, runFlags{}, passthrough, workingTree)
	if err != nil {
		return err
	}

	if detach {
		fmt.Fprintln(os.Stdout, resp.RunID)
		return nil
	}

	ctx := context.Background()
	fmt.Fprintf(os.Stderr, "triggered %s on %s as %s (status=%s); following...\n",
		pipelineName, prof.Name, resp.RunID, resp.Status)

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
