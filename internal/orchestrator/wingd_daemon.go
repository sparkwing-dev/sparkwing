package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunStoreReapInterval is how often the daemon host reaps the runs store
// while the daemon serves. It matches the controller's reaper cadence.
const RunStoreReapInterval = 10 * time.Second

// WingdOptions are the host's choices when it runs the admission daemon in
// this process. Zero values take the daemon's own defaults.
type WingdOptions struct {
	Home             string
	Version          string
	HeadroomFraction float64
	Budget           wingd.Budget
	BudgetSource     wingd.BudgetSource
	BudgetOrigin     string
	Logf             func(format string, args ...any)
}

// RunWingdDaemon runs the admission daemon until ctx ends or it idles out.
// The daemon reaches the runs store through one handle held for its
// lifetime, and this process reaps that store every RunStoreReapInterval
// while the daemon serves, which is the only reaper a machine with no
// controller has. Losing the election is not an error: another daemon owns
// the socket.
func RunWingdDaemon(ctx context.Context, opts WingdOptions) error {
	return runWingdDaemon(ctx, opts, nil)
}

func runWingdDaemon(ctx context.Context, opts WingdOptions, tune func(*wingd.Config, *HeldRunStore)) error {
	runs, err := NewHeldRunStore(opts.Home)
	if err != nil {
		return err
	}
	defer func() { _ = runs.Close() }()
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	cfg := wingd.Config{
		Home:               opts.Home,
		Version:            opts.Version,
		HeadroomFraction:   opts.HeadroomFraction,
		Budget:             opts.Budget,
		BudgetSource:       opts.BudgetSource,
		BudgetOrigin:       opts.BudgetOrigin,
		Runs:               runs,
		StoreSchemaVersion: store.ExpectedSchemaVersion(),
		StoreRequirements:  store.KnownRequirements(),
		Logf:               logf,
	}
	if tune != nil {
		tune(&cfg, runs)
	}
	d, err := wingd.New(cfg)
	if err != nil {
		return err
	}
	maintCtx, stopMaintenance := context.WithCancel(ctx)
	defer stopMaintenance()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		maintainRunStore(maintCtx, runs, d.Ready(), logf)
	}()
	runErr := d.Run(ctx)
	stopMaintenance()
	<-stopped
	if runErr != nil && !errors.Is(runErr, wingd.ErrNotElected) {
		return runErr
	}
	return nil
}

// maintainRunStore reaps the runs store while the elected daemon serves.
// The first pass also reconciles runs whose process died without finishing
// them, the way a CLI run does on start.
func maintainRunStore(ctx context.Context, runs *HeldRunStore, ready <-chan struct{}, logf func(string, ...any)) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	reconciled := false
	pass := func(force bool) {
		st, err := runs.Store(force)
		if err != nil {
			if force && ctx.Err() == nil && !errors.Is(err, errRunStoreAbsent) {
				logf("runs store unavailable: %v", err)
			}
			return
		}
		if !reconciled {
			reconciled = true
			if n, err := ReconcileOrphanedLocalRuns(ctx, st, 0); err != nil {
				if ctx.Err() == nil {
					logf("reconcile orphaned local runs: %v", err)
				}
			} else if n > 0 {
				logf("reconciled %d orphaned local run(s)", n)
			}
		}
		res, err := st.MaintainConcurrency(ctx, store.ConcurrencyMaintenanceOptions{})
		if err != nil && ctx.Err() == nil {
			logf("concurrency maintenance: %v", err)
		}
		if summary := reapSummary(res); summary != "" {
			logf("reaped %s", summary)
		}
	}
	pass(true)
	ticker := time.NewTicker(RunStoreReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass(false)
		}
	}
}

func reapSummary(res store.ConcurrencyMaintenanceResult) string {
	var parts []string
	add := func(n int, what string) {
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, what))
		}
	}
	add(len(res.StaleHolders), "stale holder(s)")
	add(len(res.StaleWaiters), "stale waiter(s)")
	add(res.Promoted, "promoted waiter(s)")
	add(res.Reconciled, "reconciled key(s)")
	add(int(res.CacheExpired), "expired cache row(s)")
	add(int(res.CacheEvicted), "evicted cache row(s)")
	return strings.Join(parts, ", ")
}
