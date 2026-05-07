// IMP-016: `sparkwing runs receipt --run X` -- recompute and emit
// the per-run audit + cost receipt as JSON. Local mode reads the
// SQLite store directly and uses the resolved profile's
// cost_per_runner_hour; cluster mode (--on NAME) defers cost to the
// controller's configured rate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
	"github.com/sparkwing-dev/sparkwing/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/orchestrator/store"
	"github.com/sparkwing-dev/sparkwing/profile"
)

func runJobsReceipt(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsReceipt.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	on := fs.String("on", "", "profile name (default: current default)")
	outFmt := fs.StringP("output", "o", "", "output format: json (default)")
	asJSON := fs.Bool("json", false, "emit JSON (hidden alias for -o json)")
	_ = fs.MarkHidden("json")
	if err := parseAndCheck(cmdJobsReceipt, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	// Receipts are JSON-only today; resolveOutputFormat would default
	// to "table" which we don't support, so accept json (or empty)
	// and reject anything else explicitly.
	switch *outFmt {
	case "", "json":
	default:
		return fmt.Errorf("runs receipt: -o/--output only supports json, got %q", *outFmt)
	}
	_ = *asJSON // alias accepted for shape parity with sibling verbs.

	if *runID == "" {
		return errors.New("runs receipt: --run is required")
	}

	if *on != "" {
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "runs receipt"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.Controller, nil, prof.Token)
		body, err := c.GetRunReceipt(ctx, *runID)
		if err != nil {
			return err
		}
		// Re-encode for stable indentation; the receipt's hashes
		// commit to canonical (compact, sorted-key) bytes the server
		// already produced, so pretty-printing here doesn't break
		// receipt_sha verification.
		var v any
		if err := json.Unmarshal(body, &v); err != nil {
			return fmt.Errorf("decode receipt: %w", err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}

	if err := paths.EnsureRoot(); err != nil {
		return err
	}
	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer st.Close()
	run, err := st.GetRun(ctx, *runID)
	if err != nil {
		return err
	}
	nodes, err := st.ListNodes(ctx, *runID)
	if err != nil {
		return err
	}
	rate, source := localCostRate()
	rec := receipt.BuildReceipt(run, nodes, rate, source)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rec)
}

// localCostRate resolves the cost_per_runner_hour for the active
// profile (if any) so local-mode receipts reflect the same rate the
// operator already has configured. A missing profile yields rate=0
// and a "(none)" source, which the receipt renders as compute_cents:0.
func localCostRate() (float64, string) {
	prof, err := profile.LoadAndResolve("")
	if err != nil || prof == nil {
		return 0, "local (no profile)"
	}
	return prof.CostPerRunnerHour, fmt.Sprintf("profile:%s (cost_per_runner_hour=$%.4f)", prof.Name, prof.CostPerRunnerHour)
}
