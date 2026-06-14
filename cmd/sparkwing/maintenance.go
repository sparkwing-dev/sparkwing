package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// runMaintenance implements `sparkwing maintenance` -- the controller-free
// janitorial pass over the concurrency tables in the local state database.
// Local runs trigger the same pass inline (throttled); this command forces
// a full pass now, for cron or to reclaim a database that grew while idle.
func runMaintenance(args []string) error {
	fs := flag.NewFlagSet(cmdMaintenance.Path, flag.ContinueOnError)
	outFmt := fs.StringP("output", "o", "", "output format: pretty|json|plain")
	if err := parseAndCheck(cmdMaintenance, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}
	format, err := resolveOutputFormat(*outFmt, cmdMaintenance.Path)
	if err != nil {
		return err
	}

	paths, err := orchestrator.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.EnsureRoot(); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	st, err := store.Open(paths.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()

	res, err := st.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{})
	if err != nil {
		return fmt.Errorf("maintenance: %w", err)
	}

	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	case "plain":
		fmt.Printf("reconciled\t%d\n", res.Reconciled)
		fmt.Printf("stale_holders\t%d\n", len(res.StaleHolders))
		fmt.Printf("promoted\t%d\n", res.Promoted)
		fmt.Printf("cache_expired\t%d\n", res.CacheExpired)
		fmt.Printf("stale_waiters\t%d\n", len(res.StaleWaiters))
		fmt.Printf("cache_evicted\t%d\n", res.CacheEvicted)
		return nil
	default:
		fmt.Printf("maintenance: reconciled=%d stale_holders=%d promoted=%d cache_expired=%d stale_waiters=%d cache_evicted=%d\n",
			res.Reconciled, len(res.StaleHolders), res.Promoted, res.CacheExpired, len(res.StaleWaiters), res.CacheEvicted)
		return nil
	}
}
