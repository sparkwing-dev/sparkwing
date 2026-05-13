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
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/storage/sparkwinglogs"
)

// resolveRunsClient returns a controller client for runs verbs. When
// onFlag is empty, the local dashboard's URL (written to
// $SPARKWING_HOME/dev.env by localws) is used so the verb operates
// against the local SQLite-backed controller without needing a
// configured profile.
//
// logc, when non-nil, is the matching logs-service client for prune's
// log-blob cleanup.
func resolveRunsClient(onFlag, cmd string) (c *client.Client, logc storage.LogStore, err error) {
	if onFlag != "" {
		prof, perr := resolveProfile(onFlag)
		if perr != nil {
			return nil, nil, perr
		}
		if perr := requireController(prof, cmd); perr != nil {
			return nil, nil, perr
		}
		c = client.NewWithToken(prof.Controller, nil, prof.Token)
		if prof.Logs != "" {
			logc = sparkwinglogs.New(prof.Logs, nil, prof.Token)
		}
		return c, logc, nil
	}
	ctrlURL := orchestrator.ResolveDevEnvURL("SPARKWING_CONTROLLER_URL")
	if ctrlURL == "" {
		return nil, nil, fmt.Errorf("%s: no --on profile and no local dashboard running "+
			"(start it with `sparkwing dashboard start`, or pass --on <profile>)", cmd)
	}
	c = client.New(ctrlURL, nil)
	if logsURL := orchestrator.ResolveDevEnvURL("SPARKWING_LOGS_URL"); logsURL != "" {
		logc = sparkwinglogs.New(logsURL, nil, "")
	}
	return c, logc, nil
}

// collectRunIDs walks --run flags and returns the deduplicated id
// list in encounter order. A --run value of "-" reads ids from
// stdin (one per line). Empty/whitespace-only entries are skipped.
func collectRunIDs(flagIDs []string, stdin io.Reader) ([]string, error) {
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
	var sawDash bool
	for _, id := range flagIDs {
		if id == "-" {
			sawDash = true
			continue
		}
		add(id)
	}
	if sawDash {
		if stdin == nil {
			return nil, errors.New("--run - requested but no stdin available")
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
	fromFailed := fs.Bool("failed", false, "rerun from failed: reuse passed nodes, re-execute only failed or unreached")
	all := fs.Bool("all", false, "rerun all: re-execute every node from scratch")
	if err := parseAndCheck(cmdJobsRetry, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected positional %q (use --run, repeatable)", cmdJobsRetry.Path, rest[0])
	}
	// Force callers to make the rerun-scope choice explicit: silent
	// defaults caused operators to ship "rerun from failed" when they
	// meant "rerun all" (and vice versa) because the two are visually
	// indistinguishable in the trigger queue. Requiring one of the
	// flags makes the intent show up in the shell history.
	switch {
	case *fromFailed && *all:
		return fmt.Errorf("%s: --failed and --all are mutually exclusive", cmdJobsRetry.Path)
	case !*fromFailed && !*all:
		return fmt.Errorf("%s: pass --failed (reuse passed nodes) or --all (re-execute everything)", cmdJobsRetry.Path)
	}
	full := *all
	ids, err := collectRunIDs(*runIDs, os.Stdin)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s: at least one --run RUN_ID is required (use --run - to read ids from stdin)", cmdJobsRetry.Path)
	}
	c, _, err := resolveRunsClient(*on, cmdJobsRetry.Path)
	if err != nil {
		return err
	}

	// Reruns dispatch asynchronously in the localws consumer, so the
	// CLI returns as soon as the trigger lands. For each queued
	// retry, print a single-line "submitted" confirmation followed
	// by the matching `runs logs --follow` command so the operator
	// can copy-paste straight into the same terminal. Failures keep
	// the source id visible so the user can correlate which retry
	// blew up when several were piped in at once.
	failures := 0
	for _, srcID := range ids {
		newID, err := c.RetryRun(ctx, srcID, full)
		if err != nil {
			failures++
			fmt.Fprintf(os.Stderr, "rerun of %s failed: %v\n", srcID, err)
			continue
		}
		fmt.Fprintf(os.Stdout, "run %s submitted successfully\n", newID)
		fmt.Fprintf(os.Stdout, "follow: sparkwing runs logs --run %s --follow%s\n",
			newID, profileSuffix(*on))
	}
	if failures > 0 {
		return fmt.Errorf("retry: %d of %d failed", failures, len(ids))
	}
	return nil
}

// profileSuffix renders the trailing ` --on <name>` segment for hint
// strings only when the caller used a non-local profile, so the
// suggested command is copy-pasteable in either mode.
func profileSuffix(on string) string {
	if on == "" {
		return ""
	}
	return " --on " + on
}

func retryOne(ctx context.Context, c *client.Client, srcRunID string) runResult {
	run, err := c.GetRun(ctx, srcRunID)
	if err != nil {
		return runResult{RunID: srcRunID, Error: fmt.Sprintf("lookup: %v", err)}
	}
	// Trigger source stays as the plain string "retry" so it never
	// leaks the source run id into user-visible chips. The retry_of
	// field carries the lineage cleanly.
	resp, err := c.CreateTrigger(ctx, client.TriggerRequest{
		Pipeline: run.Pipeline,
		Args:     run.Args,
		Trigger: client.TriggerMeta{
			Source: "retry",
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
	runIDs := multiFlagVar(fs, "run", "run id to cancel (repeatable; use --run - to read ids from stdin)")
	on := fs.String("on", "", "profile name (default: current default)")
	if err := parseAndCheck(cmdJobsCancel, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected positional %q (use --run, repeatable)", cmdJobsCancel.Path, rest[0])
	}
	ids, err := collectRunIDs(*runIDs, os.Stdin)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s: at least one --run RUN_ID is required (use --run - to read ids from stdin)", cmdJobsCancel.Path)
	}
	c, _, err := resolveRunsClient(*on, cmdJobsCancel.Path)
	if err != nil {
		return err
	}
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
	runIDs := multiFlagVar(fs, "run", "specific run id to prune (repeatable; use --run - to read ids from stdin)")
	if err := parseAndCheck(cmdJobsPrune, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected positional %q (use --run, repeatable)", cmdJobsPrune.Path, rest[0])
	}
	explicitIDs, err := collectRunIDs(*runIDs, os.Stdin)
	if err != nil {
		return err
	}
	if len(explicitIDs) == 0 && *olderThan <= 0 {
		return errors.New("runs prune: either --older-than DUR or --run RUN_ID is required")
	}
	if len(explicitIDs) > 0 && *olderThan > 0 {
		return errors.New("runs prune: --run and --older-than are mutually exclusive")
	}
	c, logc, err := resolveRunsClient(*on, cmdJobsPrune.Path)
	if err != nil {
		return err
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
