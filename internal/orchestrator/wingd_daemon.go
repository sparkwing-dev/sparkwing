package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunStoreReapInterval is how often the daemon host reaps the runs store
// while the daemon serves. It matches the controller's reaper cadence.
const RunStoreReapInterval = 10 * time.Second

// safety: a pass shares the writing handle's single connection with finalize,
// so it is bounded rather than left to the store's busy timeout, and the next
// tick retries whatever it dropped.
const runStoreReapTimeout = 30 * time.Second

// WingdOptions are the host's choices when it runs the admission daemon in
// this process. Zero values take the daemon's own defaults.
type WingdOptions struct {
	Home             string
	Version          string
	HeadroomFraction float64
	Budget           wingd.Budget
	BudgetSource     wingd.BudgetSource
	BudgetOrigin     string
	// ArtifactStore backs the controller API's artifact routes. Nil leaves
	// them unregistered, which is what a machine with no cache configured
	// gets from a run's own controller today.
	ArtifactStore storage.ArtifactStore
	Logger        *slog.Logger
	Logf          func(format string, args ...any)
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
	api := newWingdAPI(runs, opts.ArtifactStore, opts.Logger)
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
		ServeAPI:           api.serve,
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

func maintainRunStore(ctx context.Context, runs *HeldRunStore, ready <-chan struct{}, logf func(string, ...any)) {
	select {
	case <-ready:
	case <-ctx.Done():
		return
	}
	reconciled := false
	fault := ""
	pass := func(force bool) {
		passCtx, cancel := context.WithTimeout(ctx, runStoreReapTimeout)
		defer cancel()
		st, err := runs.Store(passCtx, force)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, errRunStoreAbsent) && err.Error() != fault {
				fault = err.Error()
				logf("runs store unavailable: %v", err)
			}
			return
		}
		fault = ""
		if !reconciled {
			reconciled = true
			if n, err := ReconcileOrphanedLocalRuns(passCtx, st, 0); err != nil {
				if ctx.Err() == nil {
					logf("reconcile orphaned local runs: %v", err)
				}
			} else if n > 0 {
				logf("reconciled %d orphaned local run(s)", n)
			}
		}
		res, err := st.MaintainConcurrency(passCtx, store.ConcurrencyMaintenanceOptions{})
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
