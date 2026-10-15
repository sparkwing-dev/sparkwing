package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func resolveRunsClient(onFlag, cmd string) (*client.Client, error) {
	if onFlag != "" {
		prof, perr := resolveProfile(onFlag)
		if perr != nil {
			return nil, perr
		}
		if perr := requireController(prof, cmd); perr != nil {
			return nil, perr
		}
		return client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken()), nil
	}
	ctrlURL := orchestrator.LocalDashboardURL("SPARKWING_CONTROLLER_URL")
	if ctrlURL == "" {
		return nil, fmt.Errorf("%s: no --profile profile and no local dashboard running "+
			"(start it with `sparkwing serve start`, or pass --profile <profile>)", cmd)
	}
	return client.NewWithToken(ctrlURL, nil, orchestrator.LocalServeToken(ctrlURL)), nil
}

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

type runResult struct {
	RunID    string `json:"run_id"`
	OK       bool   `json:"ok"`
	NewRunID string `json:"new_run_id,omitempty"`

	Note  string `json:"note,omitempty"`
	Error string `json:"error,omitempty"`
}

func runRunsRetry(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet(cmdJobsRetry.Path, flag.ContinueOnError)
	runIDs := multiFlagVar(fs, "run", "source run id (repeatable; can also be a positional or `-` for stdin)")
	on := fs.String("profile", "", "profile name for remote runs; omit for local runs")
	fromFailed := fs.Bool("failed", false, "rerun from failed: reuse passed nodes, re-execute only failed or unreached")
	all := fs.Bool("all-jobs", false, "rerun all: re-execute every node from scratch")
	if err := parseAndCheck(cmdJobsRetry, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	if rest := fs.Args(); len(rest) > 0 {
		return fmt.Errorf("%s: unexpected positional %q (use --run, repeatable)", cmdJobsRetry.Path, rest[0])
	}
	switch {
	case *fromFailed && *all:
		return fmt.Errorf("%s: --failed and --all-jobs are mutually exclusive", cmdJobsRetry.Path)
	case !*fromFailed && !*all:
		return fmt.Errorf("%s: pass --failed (reuse passed nodes) or --all-jobs (re-execute everything)", cmdJobsRetry.Path)
	}
	full := *all
	ids, err := collectRunIDs(*runIDs, os.Stdin)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("%s: at least one --run RUN_ID is required (use --run - to read ids from stdin)", cmdJobsRetry.Path)
	}
	failures := 0
	requested := len(ids)
	if *on == "" {
		var refused []runResult
		refused, ids = standaloneLocalRuns(ctx, "", ids, cmdJobsRetry.Path,
			orchestrator.StandaloneSubmitRefusal)
		for _, r := range refused {
			failures++
			fmt.Fprintf(os.Stderr, "rerun of %s failed: %s\n", r.RunID, r.Error)
		}
		if len(ids) == 0 {
			return fmt.Errorf("retry: %d of %d failed", failures, requested)
		}
		results, localErr := retryLocalRuns(ctx, "", ids)
		if localErr != nil {
			return localErr
		}
		for _, result := range results {
			failures++
			fmt.Fprintf(os.Stderr, "rerun of %s failed: %s\n", result.RunID, result.Error)
		}
		return fmt.Errorf("retry: %d of %d failed", failures, requested)
	}
	c, err := resolveRunsClient(*on, cmdJobsRetry.Path)
	if err != nil {
		return err
	}

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
		return fmt.Errorf("retry: %d of %d failed", failures, requested)
	}
	return nil
}

func retryLocalRuns(ctx context.Context, home string, ids []string) ([]runResult, error) {
	paths, err := submitPaths(home)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(paths.StateDB()); err != nil {
		return nil, fmt.Errorf("no local runs store at %s (is this the home the run is using?)", paths.StateDB())
	}
	st, err := store.OpenReadOnly(paths.StateDB())
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", paths.StateDB(), err)
	}
	results := make([]runResult, 0, len(ids))
	for _, sourceID := range ids {
		_, err := st.GetRun(ctx, sourceID)
		if err == nil {
			err = &orchestrator.RetryEnvironmentUnavailableError{RunID: sourceID}
		}
		results = append(results, runResult{RunID: sourceID, Error: err.Error()})
	}
	if err := st.Close(); err != nil {
		return results, fmt.Errorf("close %s: %w", paths.StateDB(), err)
	}
	return results, nil
}

func profileSuffix(on string) string {
	if on == "" {
		return ""
	}
	return " --profile " + on
}

func standaloneLocalRuns(
	ctx context.Context,
	home string,
	ids []string,
	verb string,
	refuse func(context.Context, orchestrator.Paths, string, string) error,
) (done []runResult, remaining []string) {
	if len(ids) == 0 {
		return nil, nil
	}
	paths, err := submitPaths(home)
	if err != nil {
		return nil, ids
	}
	stores := orchestrator.OpenStandaloneStores(ctx, paths)
	empty := stores.Empty()
	_ = stores.Close()
	if empty {
		return nil, ids
	}
	for _, id := range ids {
		if err := refuse(ctx, paths, id, verb); err != nil {
			done = append(done, runResult{RunID: id, Error: err.Error()})
			continue
		}
		remaining = append(remaining, id)
	}
	return done, remaining
}
