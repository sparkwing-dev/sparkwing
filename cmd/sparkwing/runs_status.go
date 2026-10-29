package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strconv"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/controllerwire"
	"github.com/sparkwing-dev/sparkwing/pkg/color"
)

type runStatusOptions struct {
	view, format            string
	follow, exitZero, steps bool
	width                   int
	timeout, poll           time.Duration
}

func parseRunStatus(inv clisurface.Invocation) (runStatusOptions, error) {
	opts := runStatusOptions{view: inv.Value("view"), format: inv.Output(color.IsInteractiveStdout()), follow: inv.Bool("follow"), exitZero: inv.Bool("exit-zero"), steps: inv.Bool("steps"), width: 60, poll: 3 * time.Second}
	invalid := func(message string) (runStatusOptions, error) {
		return opts, &clisurface.UsageError{Path: inv.Command.Path, Message: message}
	}
	if opts.view == "" {
		opts.view = "summary"
	}
	switch opts.view {
	case "summary", "timeline", "errors", "tree", "receipt":
	default:
		return invalid("--view must be summary|timeline|receipt|errors|tree")
	}
	if inv.Has("width") {
		opts.width, _ = strconv.Atoi(inv.Value("width"))
	}
	if opts.width < 1 {
		return invalid("--width must be positive")
	}
	if inv.Has("poll") {
		opts.poll, _ = time.ParseDuration(inv.Value("poll"))
	}
	if opts.poll <= 0 {
		return invalid("--poll must be positive")
	}
	if inv.Has("timeout") {
		opts.timeout, _ = time.ParseDuration(inv.Value("timeout"))
	}
	if opts.timeout < 0 || inv.Has("timeout") && !opts.follow {
		return invalid("--timeout requires --follow and must be nonnegative")
	}
	return opts, nil
}

func runRunsStatus(ctx context.Context, inv clisurface.Invocation) error {
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(color.IsInteractiveStdout()))
	}
	if _, err := parseRunStatus(inv); err != nil {
		return err
	}
	reader, closeTransport, err := resolveRunReader(ctx, inv.Value("profile"))
	if err != nil {
		return observationError(err)
	}
	defer closeTransport()
	return observeRunStatus(ctx, inv, reader, os.Stdout, waitStatusPoll)
}

func observeRunStatus(ctx context.Context, inv clisurface.Invocation, reader controllerwire.RunReader, out io.Writer, wait func(context.Context, time.Duration) error) error {
	opts, err := parseRunStatus(inv)
	if err != nil {
		return err
	}
	if opts.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.timeout)
		defer cancel()
	}
	if wait == nil {
		wait = waitStatusPoll
	}
	var previous *controllerwire.RunDetail
	for {
		detail, err := reader.ReadRun(ctx, inv.Value("run"))
		if err != nil {
			return observationError(err)
		}
		if detail.ID == "" || detail.Status == "" || previous != nil && previous.ID != detail.ID {
			return observationError(invalidObservation("run observation is missing its identity or changed runs"))
		}
		failedJobs := opts.view == "summary" && slices.ContainsFunc(detail.Jobs, func(job controllerwire.JobDetail) bool {
			return job.Outcome == "failed" || job.Status == "failed"
		})
		if (opts.steps || failedJobs) && opts.view != "receipt" {
			steps, err := reader.ReadSteps(ctx, detail.ID)
			if err != nil {
				return observationError(err)
			}
			detail.Steps = steps.Steps
			if !opts.steps {
				detail.Steps = slices.DeleteFunc(slices.Clone(detail.Steps), func(step controllerwire.StepDetail) bool {
					return step.Outcome != "failed" && step.Status != "failed"
				})
			}
		}
		if previous == nil || opts.view == "tree" || !reflect.DeepEqual(detail, *previous) {
			if err := renderObservedRun(ctx, reader, out, detail, opts); err != nil {
				return err
			}
			previous = &detail
		}
		if isTerminalRunStatus(detail.Status) {
			if opts.exitZero {
				return nil
			}
			return reportedExit(statusExitCode(detail.Status))
		}
		if !opts.follow {
			return reportedExit(exitErrorf(3, "run %s is %s", detail.ID, detail.Status))
		}
		if err := wait(ctx, opts.poll); err != nil {
			return observationError(fmt.Errorf("observation incomplete: %w", err))
		}
	}
}

func waitStatusPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
