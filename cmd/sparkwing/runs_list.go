package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func clientSideFilter(f orchestrator.CompiledFilter) bool {
	f.Branches, f.SHAPrefixes = nil, nil
	return f.HasAny()
}

// safety: the grouped view renders the page the ordinary listing selects,
// so filters, paging, dedupe across stores and old-controller handling are
// the listing's own; grouping happens on that page.
func listFailures(ctx context.Context, paths orchestrator.Paths, opts orchestrator.ListOpts, groupBy string, asJSON bool) error {
	if len(opts.Statuses) != 1 || opts.Statuses[0] != "failed" {
		return errors.New("runs list: --group-by reads failed runs; pass --status failed")
	}
	if opts.ByPipeline {
		return errors.New("runs list: --group-by and --by-pipeline pick different views; pass one")
	}
	switch groupBy {
	case "run":
		groupBy = ""
	case "step", "node":
	default:
		return fmt.Errorf("runs list: --group-by must be run|step|node, got %q", groupBy)
	}
	opts.RenderRows = func(rows []orchestrator.TaggedRun, nodes func(orchestrator.TaggedRun) ([]*store.Node, error)) error {
		failures := make([]failureRow, 0, len(rows))
		for _, r := range rows {
			found, err := nodes(r)
			if err != nil {
				found = nil
			}
			failures = append(failures, failureRowFromNodes(r, found))
		}
		return renderFailures(failures, groupBy, asJSON)
	}
	return orchestrator.ListJobs(ctx, paths, opts, os.Stdout)
}

func waitForMatchingRun(ctx context.Context, paths orchestrator.Paths, opts orchestrator.ListOpts, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		runs, err := orchestrator.LatestRuns(ctx, paths, opts, 1)
		if err != nil {
			return err
		}
		if len(runs) > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return exitErrorf(2, "runs list: timeout after %s with no match", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func watchNewRuns(ctx context.Context, paths orchestrator.Paths, opts orchestrator.ListOpts, asJSON bool) error {
	last := ""
	if runs, err := orchestrator.LatestRuns(ctx, paths, opts, 1); err == nil && len(runs) > 0 {
		last = runs[0].ID
	}
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		runs, err := orchestrator.LatestRuns(ctx, paths, opts, 1)
		if err != nil {
			fmt.Fprintf(os.Stderr, "watch: %v\n", err)
			continue
		}
		if len(runs) == 0 || runs[0].ID == last {
			continue
		}
		last = runs[0].ID
		if err := writeWatchedRun(os.Stdout, runs[0], asJSON, opts.Quiet); err != nil {
			return err
		}
	}
}

func writeWatchedRun(w io.Writer, run *store.Run, asJSON, quiet bool) error {
	switch {
	case quiet && asJSON:
		return json.NewEncoder(w).Encode(run.ID)
	case quiet:
		_, err := fmt.Fprintln(w, run.ID)
		return err
	case asJSON:
		return json.NewEncoder(w).Encode(store.RedactedRun(run))
	default:
		_, err := fmt.Fprintf(w, "%s  %s  %s  (%s)\n", run.ID, run.Pipeline, run.Status, relTime(run.StartedAt))
		return err
	}
}
