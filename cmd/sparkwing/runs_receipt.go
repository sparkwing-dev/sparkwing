package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/orchestrator/receipt"
	"github.com/sparkwing-dev/sparkwing/pkg/controller/client"
)

func runJobsReceipt(ctx context.Context, paths orchestrator.Paths, args []string) error {
	fs := flag.NewFlagSet(cmdJobsReceipt.Path, flag.ContinueOnError)
	runID := fs.String("run", "", "run identifier")
	on := fs.String("profile", "", "profile name; omit for local-only")
	outFmt := fs.StringP("output", "o", "", "output format: json (default)")
	if err := parseAndCheck(cmdJobsReceipt, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	switch *outFmt {
	case "", "json":
	default:
		return fmt.Errorf("runs receipt: -o/--output only supports json, got %q", *outFmt)
	}

	if *runID == "" {
		return errors.New("runs receipt: --run is required")
	}
	*runID = normalizeRunID(*runID)

	if *on != "" {
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "runs receipt"); err != nil {
			return err
		}
		c := client.NewWithToken(prof.ControllerURL(), nil, prof.ControllerToken())
		body, err := c.GetRunReceipt(ctx, *runID)
		if err != nil {
			return err
		}
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
	st, label, done, err := orchestrator.OpenStoreForRun(ctx, paths, *runID)
	if err != nil {
		return err
	}
	defer done()
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
	rec.Store = label
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(rec)
}

func localCostRate() (float64, string) {
	return 0, "local (rate not configured)"
}
