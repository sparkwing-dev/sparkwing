// `sparkwing pipeline trigger <name> --profile <p> [--detach]` submits a
// trigger to the named profile's controller for remote execution. It is
// the v0.5.0 successor to `sparkwing run --sw-profile prof`: it shares
// the trigger-creation core (createRemoteTrigger) so the wire payload is
// identical, then follows the remote run until terminal (logs when the
// profile defines a logs URL, otherwise node-status). --detach skips the
// follow and prints the run id once the trigger is registered.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
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
		return orchestrator.JobLogsRemoteWithTokens(ctx, prof.ControllerURL(), prof.ControllerURL(), prof.ControllerToken(),
			resp.RunID, orchestrator.LogsOpts{Follow: true, Format: format, JSON: format == "json"}, os.Stdout)
	}

	fmt.Fprintln(os.Stderr, color.Dim(fmt.Sprintf(
		"note: profile %q declares no logs: backend; following node status (no log bodies). "+
			"Add a logs: spec in profiles.yaml to see streaming output.", prof.Name)))
	return orchestrator.JobStatusRemote(ctx, prof.ControllerURL(), prof.ControllerToken(),
		resp.RunID, orchestrator.StatusOpts{Follow: true}, os.Stdout)
}
