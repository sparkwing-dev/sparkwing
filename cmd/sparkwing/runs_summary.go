// Handler for `sparkwing runs summary`. Rendering lives in
// orchestrator/runs_summary.go; this file is flag plumbing only.
package main

import (
	"context"
	"errors"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
)

func runJobsSummary(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsSummary.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json (default: pretty on TTY, json when piped)")
	asJSON := fs.Bool("json", false, "emit JSON (alias for -o json)")
	pretty := fs.Bool("pretty", false, "force the human-readable view even when piped (alias for -o table)")
	on := fs.String("on", "", "profile name; omit for local-only")
	if err := parseAndCheck(cmdJobsSummary, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	*runID = normalizeRunID(*runID)
	resolvedFmt, err := resolveTTYAwareOutput(*outFmt, fs.Changed("output"), *asJSON, *pretty, cmdJobsSummary.Path)
	if err != nil {
		return err
	}
	opts := orchestrator.SummaryOpts{JSON: resolvedFmt == "json"}
	if *on == "" {
		return orchestrator.RunSummaryLocal(ctx, paths, *runID, opts, os.Stdout)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdJobsSummary.Path); err != nil {
		return err
	}
	return orchestrator.RunSummaryRemote(ctx, prof.Controller, prof.Token, *runID, opts, os.Stdout)
}
