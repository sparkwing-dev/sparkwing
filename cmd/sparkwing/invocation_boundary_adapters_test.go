package main

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/internal/pipelineentry"
	"github.com/sparkwing-dev/sparkwing/internal/repos"
	"github.com/sparkwing-dev/sparkwing/internal/sparks"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/journal"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runSecretArgs(args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"secrets"}, args...))
	if err != nil {
		return err
	}
	return runSecret(inv)
}

func runProfilesArgs(args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"profiles"}, args...))
	if err != nil {
		return err
	}
	return runProfiles(inv)
}

func runQueueCommandArgs(ctx context.Context, args []string, out io.Writer, deps queueDependencies) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"queue"}, args...))
	if err != nil {
		return err
	}
	return runQueueCommand(ctx, inv, out, deps)
}

func runDashboardCommandArgs(ctx context.Context, args []string, out io.Writer, goos string, resolve func(context.Context) (dashboardServices, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"dashboard"}, args...))
	if err != nil {
		return err
	}
	return runDashboardCommand(ctx, inv, out, goos, resolve)
}

func runDoctorCommandArgs(ctx context.Context, args []string, out io.Writer, deps doctorDependencies) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"doctor"}, args...))
	if err != nil {
		return err
	}
	return runDoctorCommand(ctx, inv, out, deps)
}

func runDoctorArgs(args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"doctor"}, args...))
	if err != nil {
		return err
	}
	return runDoctor(inv)
}

func runCacheWarmCommandArgs(ctx context.Context, args []string, out io.Writer, deps cacheWarmDependencies) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"cache", "warm"}, args...))
	if err != nil {
		return err
	}
	return runCacheWarmCommand(ctx, inv, out, deps)
}

func runOperatorTeamsCommandArgs(ctx context.Context, args []string, out io.Writer, connect operatorConnector) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"operator", "teams"}, args...))
	if err != nil {
		return err
	}
	return runOperatorTeamsCommand(ctx, inv, out, connect)
}

func runOperatorLimitsCommandArgs(ctx context.Context, args []string, out io.Writer, connect operatorConnector) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"operator", "limits"}, args...))
	if err != nil {
		return err
	}
	return runOperatorLimitsCommand(ctx, inv, out, connect)
}

func runOperatorObjectStoreCommandArgs(ctx context.Context, args []string, out io.Writer, connect operatorConnector) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"operator", "object-store"}, args...))
	if err != nil {
		return err
	}
	return runOperatorObjectStoreCommand(ctx, inv, out, connect)
}

func runScheduleCommandArgs(ctx context.Context, args []string, out io.Writer, command scheduleCommand) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"schedules"}, args...))
	if err != nil {
		return err
	}
	return runScheduleCommand(ctx, inv, out, command)
}

func runOverflowCommandArgs(ctx context.Context, args []string, out io.Writer, connect func(context.Context, string) (controllerwire.OverflowManager, func(), error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"overflow"}, args...))
	if err != nil {
		return err
	}
	return runOverflowCommand(ctx, inv, out, connect)
}

func runRunsRetryCommandArgs(ctx context.Context, args []string, input io.Reader, out io.Writer, resolve func(context.Context, string) (retryConnection, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", "retry"}, args...))
	if err != nil {
		return err
	}
	return runRunsRetryCommand(ctx, inv, input, out, resolve)
}

func runSDKInstallCommandArgs(ctx context.Context, args []string, version string, input io.Reader, out, diagnostics io.Writer, install func(context.Context, string, io.Reader, io.Writer, io.Writer) error) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"__sdk-install"}, args...))
	if err != nil {
		return err
	}
	return runSDKInstallCommand(ctx, inv, version, input, out, diagnostics, install)
}

func runWingdRunCommandArgs(ctx context.Context, args []string, out io.Writer, options wingd.LocalStartOptions, resolve func(context.Context, wingd.LocalStartOptions) (wingd.LocalRuntime, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"wingd", "run"}, args...))
	if err != nil {
		return err
	}
	return runWingdRunCommand(ctx, inv, out, options, resolve)
}

func runScheduleTickCommandArgs(ctx context.Context, args []string, out io.Writer, resolve func(context.Context, string) (localScheduleServices, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"__tick"}, args...))
	if err != nil {
		return err
	}
	return runScheduleTickCommand(ctx, inv, out, resolve)
}

func runLocalHostCommandArgs(ctx context.Context, args []string, out io.Writer, deps localRunDependencies) (resultErr error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"__host-trigger"}, args...))
	if err != nil {
		return err
	}
	return runLocalHostCommand(ctx, inv, out, deps)
}

func runDaemonLifecycleCommandArgs(ctx context.Context, args []string, out io.Writer, resolve func(context.Context) (dashboardServices, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"daemon"}, args...))
	if err != nil {
		return err
	}
	return runDaemonLifecycleCommand(ctx, inv, out, resolve)
}

func runDaemonEventsCommandArgs(args []string, out, diagnostics io.Writer, now time.Time, read func() (string, []journal.Record, int, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"daemon", "events"}, args...))
	if err != nil {
		return err
	}
	return runDaemonEventsCommand(inv, out, diagnostics, now, read)
}

func runDaemonEventsArgs(args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"daemon", "events"}, args...))
	if err != nil {
		return err
	}
	return runDaemonEvents(inv)
}

func runDebugResumeArgs(ctx context.Context, args []string, out io.Writer, resolve func(context.Context, string) (debugResumer, error)) (resultErr error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"debug", "resume"}, args...))
	if err != nil {
		return err
	}
	return runDebugResume(ctx, inv, out, resolve)
}

func runReviewCommandArgs(ctx context.Context, args []string, out io.Writer, resolve func(context.Context, string) (controllerwire.RunReview, func(), error)) error {
	inv, err := clisurface.Parse("sparkwing", args)
	if err != nil {
		return err
	}
	return runReviewCommand(ctx, inv, out, resolve)
}

func runRunsLogsArgs(ctx context.Context, verb string, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runs", verb}, args...))
	if err != nil {
		return err
	}
	return runRunsLogs(ctx, inv)
}

func runInternalCompleteArgs(args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"__complete"}, args...))
	if err != nil {
		return err
	}
	return runInternalComplete(inv)
}

func (c upgradeCommand) runArgs(ctx context.Context, args []string) (result error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"upgrade"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return c.run(ctx, inv)
	}
	if checkout := inv.Value("checkout"); checkout != "" {
		previous, err := os.Getwd()
		if err != nil {
			return err
		}
		if err := os.Chdir(checkout); err != nil {
			return err
		}
		defer func() { result = errors.Join(result, os.Chdir(previous)) }()
	}
	return c.run(ctx, inv)
}

func (c updateCommand) runArgs(ctx context.Context, args []string) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"update"}, args...))
	if err != nil {
		return err
	}
	return c.run(ctx, inv)
}

func runPipelineDescribeCommandArgs(args []string, withEntry func(string, func(context.Context, pipelineentry.Command, string) error) error) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "describe"}, args...))
	if err != nil {
		return err
	}
	return runPipelineDescribeCommand(inv, func(name string, inspect func(context.Context, pipelineInspection, string) error) error {
		return withEntry(name, func(ctx context.Context, command pipelineentry.Command, directory string) error {
			entry, err := inspectionCommand(command, directory, name)
			if err != nil {
				return err
			}
			return inspect(ctx, entry, directory)
		})
	})
}

func parseRunFlagsArgs(args []string) (runFlags, []string) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"run", "pipeline"}, args...))
	if err != nil {
		return runFlags{parseErr: err}, nil
	}
	return runFlagsFromInvocation(inv)
}

func dispatchRunWithEngineArgs(args []string, selectEngine func(string, func()) error) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"run"}, args...))
	if err != nil {
		return err
	}
	return dispatchRunWithEngine(inv, selectEngine)
}

func parseTriggerFlagsArgs(args []string) (triggerFlags, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "trigger"}, args...))
	if err != nil {
		return triggerFlags{}, err
	}
	return triggerFlagsFromInvocation(inv)
}

func runTriggerCommandArgs(ctx context.Context, args []string, out io.Writer, prepare func(context.Context, triggerFlags) (controllerwire.CreateRunRequest, error), connect func(context.Context, string) (client.EngineOptions, func(), error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "trigger"}, args...))
	if err != nil {
		return err
	}
	return runTriggerCommand(ctx, inv, out, prepare, connect)
}

func runSparksInflateCommandArgs(ctx context.Context, args []string, out io.Writer, locate func() (string, error), vendor func(context.Context, string, string) (*sparks.VendorResult, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "sparks", "inflate"}, args...))
	if err != nil {
		return err
	}
	return runSparksInflateCommand(ctx, inv, out, locate, vendor)
}

func runReposUpdateWithArgs(args []string, readFleet func(string) ([]repos.Repo, error), update func([]repos.Repo, repos.UpdateConfig) []repos.Verdict) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"repos", "update"}, args...))
	if err != nil {
		return err
	}
	return runReposUpdateWith(inv, readFleet, update)
}

func prepareRunnerEnrollmentArgs(args []string) (runnerEnrollment, *namedProfile, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runners", "add"}, args...))
	if err != nil {
		return runnerEnrollment{}, nil, err
	}
	return prepareRunnerEnrollment(inv)
}

func prepareRunnerRemovalArgs(args []string) (runnerRemoval, *namedProfile, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"runners", "remove"}, args...))
	if err != nil {
		return runnerRemoval{}, nil, err
	}
	return prepareRunnerRemoval(inv)
}

func parsePipelinePlanArgs(args []string) (pipelinePlanArgs, bool, error) {
	inv, err := clisurface.Parse("sparkwing", append([]string{"pipeline", "plan"}, args...))
	if err != nil {
		return pipelinePlanArgs{}, false, err
	}
	return pipelinePlanOptions(inv), inv.Help(), nil
}
