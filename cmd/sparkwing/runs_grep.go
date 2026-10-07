package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
)

type logSearchFlags struct {
	pipelines, statuses, branches, shas *[]string
	startedAfter, startedBefore         *string
	limit, maxMatches                   *int
	quiet                               *bool
}

var logSearchFlagNames = []string{"pipeline", "status", "branch", "sha", "started-after", "started-before", "limit", "max-matches", "quiet"}

func addLogSearchFlags(fs *flag.FlagSet) logSearchFlags {
	return logSearchFlags{
		pipelines:     multiFlagVar(fs, "pipeline", "with --grep and no --run: filter runs by pipeline (repeatable; prefix `!` to exclude)"),
		statuses:      multiFlagVar(fs, "status", "with --grep and no --run: filter runs by status (repeatable; prefix `!` to exclude)"),
		branches:      multiFlagVar(fs, "branch", "with --grep and no --run: filter runs by git branch (repeatable; prefix `!` to exclude)"),
		shas:          multiFlagVar(fs, "sha", "with --grep and no --run: filter runs by git sha prefix (repeatable; prefix `!` to exclude)"),
		startedAfter:  fs.String("started-after", "", "with --grep and no --run: only runs whose StartedAt >= this"),
		startedBefore: fs.String("started-before", "", "with --grep and no --run: only runs whose StartedAt <= this"),
		limit:         fs.Int("limit", 50, "with --grep and no --run: max candidate runs to scan"),
		maxMatches:    fs.Int("max-matches", 5, "with --grep and no --run: per-node match cap (0 = no cap)"),
		quiet:         fs.BoolP("quiet", "q", false, "with --grep and no --run: print only the unique matching run ids"),
	}
}

func (f logSearchFlags) changed(fs *flag.FlagSet) bool {
	for _, name := range logSearchFlagNames {
		if fs.Changed(name) {
			return true
		}
	}
	return false
}

func searchRunLogs(ctx context.Context, paths orchestrator.Paths, fs *flag.FlagSet, f logSearchFlags, pattern string, since time.Duration, profileName, format string) error {
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("runs logs: unexpected positional %q", rest[0])
	}
	pipelineInc, pipelineExc := orchestrator.SplitExcludes(*f.pipelines)
	statusInc, statusExc := orchestrator.SplitExcludes(*f.statuses)
	branchInc, branchExc := orchestrator.SplitExcludes(*f.branches)
	shaInc, shaExc := orchestrator.SplitExcludes(*f.shas)
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
		{*f.startedAfter, &compiled.StartedAfter, "started-after"},
		{*f.startedBefore, &compiled.StartedBefore, "started-before"},
	} {
		if ts.raw == "" {
			continue
		}
		t, err := orchestrator.ParseLooseDate(ts.raw)
		if err != nil {
			return fmt.Errorf("runs logs: --%s: %w", ts.name, err)
		}
		*ts.into = t
	}
	opts := orchestrator.GrepOpts{
		Pattern:    pattern,
		Limit:      *f.limit,
		MaxMatches: *f.maxMatches,
		JSON:       format == "json",
		Quiet:      *f.quiet,
		Pipelines:  pipelineInc,
		Statuses:   statusInc,
		Since:      since,
		Filter:     compiled,
	}
	prof, err := resolveProfileFlag(profileName)
	if err != nil {
		return err
	}
	if prof == nil || (profileName == "" && prof.ControllerURL() == "") {
		return orchestrator.RunGrepLocal(ctx, paths, opts, os.Stdout)
	}
	if err := requireController(prof, "runs logs --grep"); err != nil {
		return err
	}
	if prof.ControllerURL() == "" {
		return errors.New("runs logs --grep: profile must carry a controller URL")
	}
	return orchestrator.RunGrepRemote(ctx, prof.ControllerURL(), prof.ExplicitLogsURL(), prof.ControllerToken(), opts, os.Stdout)
}
