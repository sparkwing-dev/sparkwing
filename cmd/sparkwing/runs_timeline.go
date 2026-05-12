// Handler for `sparkwing runs timeline`. The rendering lives in
// orchestrator/runs_timeline.go; this file is just flag plumbing.
package main

import (
	"context"
	"errors"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
)

func runJobsTimeline(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsTimeline.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	steps := fs.Bool("steps", false, "include per-step rows under each node")
	width := fs.Int("width", 60, "bar width in characters")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json (default: pretty on TTY, json when piped)")
	asJSON := fs.Bool("json", false, "emit JSON (alias for -o json)")
	pretty := fs.Bool("pretty", false, "force the human-readable waterfall even when piped (alias for -o table)")
	on := fs.String("on", "", "profile name; omit for local-only")
	if err := parseAndCheck(cmdJobsTimeline, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	*runID = normalizeRunID(*runID)
	resolvedFmt, err := resolveTTYAwareOutput(*outFmt, fs.Changed("output"), *asJSON, *pretty, cmdJobsTimeline.Path)
	if err != nil {
		return err
	}
	opts := orchestrator.TimelineOpts{
		Width:        *width,
		IncludeSteps: *steps,
		JSON:         resolvedFmt == "json",
	}
	if *on == "" {
		return orchestrator.RunTimeline(ctx, paths, *runID, opts, os.Stdout)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdJobsTimeline.Path); err != nil {
		return err
	}
	return orchestrator.RunTimelineRemote(ctx, prof.Controller, prof.Token, *runID, opts, os.Stdout)
}
