// `sparkwing pipeline trigger <name> --profile <p> [--detach]` submits a
// trigger to the named profile's controller for remote execution. It is
// the v0.5.0 successor to `sparkwing run --sw-profile prof`: it shares
// the trigger-creation core (createRemoteTrigger) so the wire payload is
// identical, then follows the remote run until terminal (logs when the
// profile defines a logs URL, otherwise node-status) and exits on the
// run's outcome, the way a local `sparkwing run` does. --detach skips
// the follow and prints the run id once the trigger is registered.
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

// parseTriggerFlags splits `pipeline trigger` args into the pipeline
// name (first positional), the recognized flags (--profile, --detach),
// and a passthrough slice of everything else (pipeline-typed args
// forwarded to the trigger payload, same shape as `sparkwing run`).
// Returns wantHelp=true for -h/--help/help.
func parseTriggerFlags(args []string) (pipelineName, profileName string, detach, wantHelp bool, passthrough []string, err error) {
	if len(args) == 0 {
		return "", "", false, false, nil, errors.New("pipeline name required (e.g. `sparkwing pipeline trigger release --profile prod`)")
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return "", "", false, true, nil, nil
	}
	pipelineName = args[0]
	if strings.HasPrefix(pipelineName, "-") {
		return "", "", false, false, nil, fmt.Errorf("pipeline name must come first; got flag %q", pipelineName)
	}

	rest := args[1:]
	i := 0
	for i < len(rest) {
		a := rest[i]
		switch {
		case a == "-h" || a == "--help":
			return "", "", false, true, nil, nil
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
		default:
			passthrough = append(passthrough, a)
			i++
		}
	}
	return pipelineName, profileName, detach, false, passthrough, nil
}

func runPipelineTrigger(args []string) error {
	pipelineName, profileName, detach, wantHelp, passthrough, err := parseTriggerFlags(args)
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

	resp, err := createRemoteTrigger(prof, pipelineName, triggerSource("pipeline-trigger"), runFlags{}, passthrough)
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

// remoteFollowExit is the remote counterpart of the local exit check:
// once the follow ends, read the triggered run's outcome and map it
// onto the exit contract a local `sparkwing run` already follows --
// success exits 0, failed and cancelled exit 1.
//
// The failure summary goes to stderr on both arms. The log arm streams
// bodies to stdout, so stderr is what survives a `> run.log` redirect;
// the status arm renders to stdout as it polls, but the frame it
// painted last can predate the terminal transition (its render and its
// terminality check are two separate reads), so the authoritative
// block is reprinted rather than assumed. On a terminal that means one
// duplicated block on the status arm, which is the cheaper mistake:
// the alternative loses the failure detail entirely under redirection.
//
// followErr is whatever ended the follow. A dropped stream is not
// itself a verdict: when the run still reads terminal, that outcome
// wins and the stream error is demoted to a note; otherwise the
// outcome is unknown and exits 3, never 1, so a controller that 503s
// during a rolling restart is never reported as a failed run.
func remoteFollowExit(ctx context.Context, prof *profile.Profile, runID string, followErr error) error {
	if followErr != nil {
		fmt.Fprintf(os.Stderr, "note: follow of %s ended early (%v); reading the run's status\n", runID, followErr)
	}
	status, fetchErr := orchestrator.RemoteRunOutcome(ctx, prof.ControllerURL(), prof.ControllerToken(), runID, os.Stderr)
	return followExitResult(prof.Name, runID, status, fetchErr, followErr)
}

// followExitResult maps an observed post-follow run state onto the CLI
// exit contract. A status that could not be read, or a follow that
// ended with the run still non-terminal (a dropped connection, a
// cancelled context), is reported as unknown and exits 3 -- the code
// `jobs wait` uses for a failed fetch -- because guessing either way
// would hand CI a verdict the CLI never observed.
func followExitResult(profileName, runID, status string, fetchErr, followErr error) error {
	if fetchErr != nil || !isTerminalRunStatus(status) {
		return exitError(3, unknownOutcomeError(profileName, runID, status, fetchErr, followErr))
	}
	// statusExitCode owns the shared success -> 0 / anything else -> 1
	// mapping; the trigger only restates it with the run id, the way
	// `jobs wait` names the run it gave up on.
	if err := statusExitCode(status); err != nil {
		return exitError(exitCodeFor(err), fmt.Errorf("pipeline trigger: run %s: %s", runID, status))
	}
	return nil
}

// unknownOutcomeError says what the CLI last saw, why it stopped
// seeing it, and the one command that answers the question later. A
// run whose outcome is unknown is frequently still executing, so the
// message must not read like a failure.
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
