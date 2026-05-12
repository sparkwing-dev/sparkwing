// Handlers for `sparkwing runs retry` / `cancel` / `prune`.
//
// All three accept run ids from any of: --run (repeatable), positional
// args, or stdin (passed as the literal positional `-`). Failures on
// individual ids do not abort the batch -- the verb prints a per-id
// status line and exits non-zero only when at least one id failed.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

// collectRunIDs walks --run flags, positional args, and stdin (when
// the positional `-` token is present) and returns the deduplicated
// id list in encounter order. Empty lines from stdin are skipped.
func collectRunIDs(flagIDs []string, positional []string, stdin io.Reader) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		id := normalizeRunID(raw)
		if seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	for _, id := range flagIDs {
		add(id)
	}
	var sawDash bool
	for _, p := range positional {
		if p == "-" {
			sawDash = true
			continue
		}
		add(p)
	}
	if sawDash {
		if stdin == nil {
			return nil, errors.New("stdin requested with '-' but no stream available")
		}
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			add(sc.Text())
		}
		if err := sc.Err(); err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
	}
	return out, nil
}

// runResult is one row in the per-id outcome list these verbs emit.
type runResult struct {
	RunID    string `json:"run_id"`
	OK       bool   `json:"ok"`
	NewRunID string `json:"new_run_id,omitempty"`
	Error    string `json:"error,omitempty"`
}

// reportResults prints per-id rows + a final summary; returns an error
// when any row failed so the shell sees a non-zero exit.
func reportResults(out io.Writer, action string, results []runResult) error {
	failures := 0
	for _, r := range results {
		switch {
		case r.OK && r.NewRunID != "":
			fmt.Fprintf(out, "ok   %s -> %s\n", r.RunID, r.NewRunID)
		case r.OK:
			fmt.Fprintf(out, "ok   %s\n", r.RunID)
		default:
			failures++
			fmt.Fprintf(out, "fail %s: %s\n", r.RunID, r.Error)
		}
	}
	successes := len(results) - failures
	fmt.Fprintf(out, "%s: %d ok, %d failed\n", action, successes, failures)
	if failures > 0 {
		return fmt.Errorf("%s: %d of %d failed", action, failures, len(results))
	}
	return nil
}

// ---- retry ---------------------------------------------------------

func runRunsRetry(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdJobsRetry.Path, flag.ContinueOnError)
	runIDs := multiFlagVar(fs, "run", "source run id (repeatable; can also be a positional or `-` for stdin)")
	on := fs.String("on", "", "profile name (default: current default)")
	if err := parseAndCheck(cmdJobsRetry, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ids, err := collectRunIDs(*runIDs, fs.Args(), os.Stdin)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s: at least one run id is required (--run, positional, or `-` for stdin)", cmdJobsRetry.Path)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdJobsRetry.Path); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)

	results := make([]runResult, 0, len(ids))
	for _, srcID := range ids {
		res := retryOne(ctx, c, srcID)
		results = append(results, res)
	}
	return reportResults(os.Stdout, "retry", results)
}

func retryOne(ctx context.Context, c *client.Client, srcRunID string) runResult {
	run, err := c.GetRun(ctx, srcRunID)
	if err != nil {
		return runResult{RunID: srcRunID, Error: fmt.Sprintf("lookup: %v", err)}
	}
	resp, err := c.CreateTrigger(ctx, client.TriggerRequest{
		Pipeline: run.Pipeline,
		Args:     run.Args,
		Trigger: client.TriggerMeta{
			Source: "retry:" + srcRunID,
		},
		Git:     client.GitMeta{Branch: run.GitBranch, SHA: run.GitSHA},
		RetryOf: srcRunID,
	})
	if err != nil {
		return runResult{RunID: srcRunID, Error: fmt.Sprintf("trigger: %v", err)}
	}
	return runResult{RunID: srcRunID, OK: true, NewRunID: resp.RunID}
}

// ---- cancel --------------------------------------------------------

func runRunsCancel(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdJobsCancel.Path, flag.ContinueOnError)
	runIDs := multiFlagVar(fs, "run", "run id to cancel (repeatable; can also be a positional or `-` for stdin)")
	on := fs.String("on", "", "profile name (default: current default)")
	if err := parseAndCheck(cmdJobsCancel, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	ids, err := collectRunIDs(*runIDs, fs.Args(), os.Stdin)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s: at least one run id is required (--run, positional, or `-` for stdin)", cmdJobsCancel.Path)
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdJobsCancel.Path); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	results := make([]runResult, 0, len(ids))
	for _, id := range ids {
		if err := c.CancelRun(ctx, id); err != nil {
			results = append(results, runResult{RunID: id, Error: err.Error()})
			continue
		}
		results = append(results, runResult{RunID: id, OK: true})
	}
	return reportResults(os.Stdout, "cancel", results)
}

// ---- prune ---------------------------------------------------------

func runRunsPrune(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdJobsPrune.Path, flag.ContinueOnError)
	on := fs.String("on", "", "profile name (default: current default)")
	olderThan := fs.Duration("older-than", 0, "prune runs older than this (e.g. 7d, 48h)")
	dryRun := fs.Bool("dry-run", false, "list matching runs without deleting")
	runIDs := multiFlagVar(fs, "run", "specific run id to prune (repeatable; positional and `-` for stdin also accepted)")
	if err := parseAndCheck(cmdJobsPrune, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	explicitIDs, err := collectRunIDs(*runIDs, fs.Args(), os.Stdin)
	if err != nil {
		return err
	}
	if len(explicitIDs) == 0 && *olderThan <= 0 {
		return errors.New("runs prune: either --older-than DUR or --run RUN_ID (or positional/stdin ids) is required")
	}
	if len(explicitIDs) > 0 && *olderThan > 0 {
		return errors.New("runs prune: --run and --older-than are mutually exclusive")
	}
	prof, err := resolveProfile(*on)
	if err != nil {
		return err
	}
	if err := requireController(prof, cmdJobsPrune.Path); err != nil {
		return err
	}
	c := client.NewWithToken(prof.Controller, nil, prof.Token)
	var logc storage.LogStore
	if prof.Logs != "" {
		logc = sparkwinglogs.New(prof.Logs, nil, prof.Token)
	}

	victims := explicitIDs
	if len(victims) == 0 {
		runs, err := c.ListRuns(ctx, store.RunFilter{Limit: 10000})
		if err != nil {
			return fmt.Errorf("list runs: %w", err)
		}
		cutoff := time.Now().Add(-*olderThan)
		for _, r := range runs {
			if !r.StartedAt.Before(cutoff) {
				continue
			}
			if r.Status != "success" && r.Status != "failed" && r.Status != "cancelled" {
				continue
			}
			victims = append(victims, r.ID)
		}
	}
	if len(victims) == 0 {
		fmt.Fprintln(os.Stdout, "no runs match prune criteria")
		return nil
	}
	if *dryRun {
		fmt.Fprintf(os.Stdout, "would prune %d run(s):\n", len(victims))
		for _, id := range victims {
			fmt.Fprintln(os.Stdout, "  "+id)
		}
		return nil
	}
	results := make([]runResult, 0, len(victims))
	for _, id := range victims {
		if err := c.DeleteRun(ctx, id); err != nil {
			results = append(results, runResult{RunID: id, Error: err.Error()})
			continue
		}
		if logc != nil {
			if err := logc.DeleteRun(ctx, id); err != nil {
				fmt.Fprintf(os.Stderr, "warn: logs delete %s: %v\n", id, err)
			}
		}
		results = append(results, runResult{RunID: id, OK: true})
	}
	return reportResults(os.Stdout, "prune", results)
}
