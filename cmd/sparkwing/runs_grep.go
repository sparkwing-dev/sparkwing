// Handler for `sparkwing runs grep PATTERN`. Search engine lives in
// orchestrator/runs_grep.go; this file is just flag plumbing.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/orchestrator"
)

func runJobsGrep(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsGrep.Path, flag.ContinueOnError)
	pipelines := multiFlagVar(fs, "pipeline", "filter by pipeline (repeatable; prefix `!` to exclude)")
	statuses := multiFlagVar(fs, "status", "filter by status (repeatable; prefix `!` to exclude)")
	branches := multiFlagVar(fs, "branch", "filter by git branch (repeatable; prefix `!` to exclude)")
	shas := multiFlagVar(fs, "sha", "filter by git sha prefix (repeatable; prefix `!` to exclude)")
	since := fs.Duration("since", 0, "only runs newer than this (e.g. 1h, 24h, 7d)")
	startedAfter := fs.String("started-after", "", "only runs whose StartedAt >= this")
	startedBefore := fs.String("started-before", "", "only runs whose StartedAt <= this")
	limit := fs.Int("limit", 50, "max candidate runs to scan")
	maxMatches := fs.Int("max-matches", 5, "per-node match cap (0 = no cap)")
	outFmt := fs.StringP("output", "o", "", "output format: table|json")
	quiet := fs.BoolP("quiet", "q", false, "print only the unique matching run ids")
	on := fs.String("on", "", "profile name; omit for local-only")
	if err := parseAndCheck(cmdJobsGrep, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("%s: PATTERN positional is required", cmdJobsGrep.Path)
	}
	if len(rest) > 1 {
		return fmt.Errorf("%s: only one PATTERN positional supported (got %d)", cmdJobsGrep.Path, len(rest))
	}
	pattern := rest[0]
	switch *outFmt {
	case "", "table", "json":
	default:
		return fmt.Errorf("%s: -o/--output must be table or json, got %q", cmdJobsGrep.Path, *outFmt)
	}

	pipelineInc, pipelineExc := orchestrator.SplitExcludes(*pipelines)
	statusInc, statusExc := orchestrator.SplitExcludes(*statuses)
	branchInc, branchExc := orchestrator.SplitExcludes(*branches)
	shaInc, shaExc := orchestrator.SplitExcludes(*shas)

	compiled := orchestrator.CompiledFilter{
		Branches:       branchInc,
		BranchExcludes: branchExc,
		SHAPrefixes:    shaInc,
		SHAExcludes:    shaExc,
		StatusExcludes: statusExc,
		PipelineExcl:   pipelineExc,
	}
	for _, ts := range []struct {
		raw  string
		into *time.Time
		name string
	}{
		{*startedAfter, &compiled.StartedAfter, "started-after"},
		{*startedBefore, &compiled.StartedBefore, "started-before"},
	} {
		if ts.raw == "" {
			continue
		}
		t, err := orchestrator.ParseLooseDate(ts.raw)
		if err != nil {
			return fmt.Errorf("%s: --%s: %w", cmdJobsGrep.Path, ts.name, err)
		}
		*ts.into = t
	}

	opts := orchestrator.GrepOpts{
		Pattern:    pattern,
		Limit:      *limit,
		MaxMatches: *maxMatches,
		JSON:       *outFmt == "json",
		Quiet:      *quiet,
		Pipelines:  pipelineInc,
		Statuses:   statusInc,
		Since:      *since,
		Filter:     compiled,
	}
	if *on == "" {
		return orchestrator.RunGrepLocal(ctx, paths, opts, os.Stdout)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdJobsGrep.Path); err != nil {
		return err
	}
	if prof.Logs == "" {
		return fmt.Errorf("%s: profile %q must carry a logs URL for remote grep", cmdJobsGrep.Path, prof.Name)
	}
	return orchestrator.RunGrepRemote(ctx, prof.Controller, prof.Logs, prof.Token, opts, os.Stdout)
}
