package orchestrator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/supervise"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// RunWingd serves the hidden `wingd` subcommands for installed binaries
// that host the local admission daemon: sparkwing-runner, whose
// in-process client self-spawns when it routes controller work through
// the local daemon, dispatches here.
//
// Both verbs are served, because the client's self-spawn starts `wingd
// supervise` and that supervisor re-execs `wingd run`: a binary that
// serves only one half answers its own spawn with a usage error and
// never brings a daemon up.
//
// Compiled pipeline binaries do not serve these verbs. The installed
// Sparkwing distribution owns daemon lifecycle; pipeline clients declare
// required capabilities and use the running daemon, but never host,
// replace, or upgrade it (see pipelineAdmission), so a repo's
// .sparkwing/go.mod pin never becomes the machine's daemon version.
func RunWingd(args []string) error {
	if len(args) > 0 && args[0] == "supervise" {
		return supervise.Run(args[1:])
	}
	return runWingdCLI(args)
}

// runWingdCLI elects and serves one daemon for a sparkwing home.
func runWingdCLI(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: wingd run|supervise [--home DIR] [--version V]")
	}
	fs := flag.NewFlagSet("wingd run", flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home (default: $SPARKWING_HOME or ~/.sparkwing)")
	version := fs.String("version", "", "binary version to advertise (default: the compiled SDK version)")
	budget := fs.String("budget", "", "machine budget cap (default: $SPARKWING_BUDGET, then the budget config file)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	v := *version
	if v == "" {
		v = sparkwingModuleVersion()
	}
	resolvedBudget, err := wingd.ResolveBudget(*budget)
	if err != nil {
		return err
	}
	d, err := wingd.New(wingd.Config{
		Home:                  *home,
		Version:               v,
		Budget:                resolvedBudget.Budget,
		BudgetSource:          resolvedBudget.Source,
		BudgetOrigin:          resolvedBudget.Origin,
		FinalizeRun:           NewOrphanRunFinalizer(*home),
		FinalizeCancelledRuns: NewCancelledRunsFinalizer(*home),
		IsRunTerminal:         NewTerminalRunChecker(*home),
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "%s wingd: %s\n",
				time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
		},
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil && !errors.Is(err, wingd.ErrNotElected) {
		return err
	}
	return nil
}

// NewOrphanRunFinalizer returns the daemon hook that finalizes a run
// row whose process died holding or awaiting admission -- the kernel
// closed the socket without an explicit release. It opens the home's
// local state DB, and flips the row to interrupted only when it is
// still running; rows already finalized, absent, or backed by a
// non-local state store are left alone.
func NewOrphanRunFinalizer(home string) func(runID string) {
	return func(runID string) {
		if err := finalizeRun(home, runID, "interrupted: run process exited without finalizing (admission connection lost)"); err != nil {
			slog.Warn("wingd: finalize orphaned run", "run_id", runID, "err", err)
		}
	}
}

// NewCancelledRunsFinalizer returns the daemon hook that persists every run
// sharing an explicitly cancelled lease in one transaction.
func NewCancelledRunsFinalizer(home string) func([]string, string) error {
	return func(runIDs []string, reason string) error {
		return finalizeRuns(home, runIDs, reason)
	}
}

func NewTerminalRunChecker(home string) func(string) (bool, error) {
	return func(runID string) (bool, error) {
		p := PathsAt(home)
		if home == "" {
			var err error
			p, err = DefaultPaths()
			if err != nil {
				return false, err
			}
		}
		if _, err := os.Stat(p.StateDB()); err != nil {
			if os.IsNotExist(err) {
				return false, nil
			}
			return false, err
		}
		st, err := store.Open(p.StateDB())
		if err != nil {
			return false, err
		}
		defer func() { _ = st.Close() }()
		run, err := st.GetRun(context.Background(), runID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return isTerminalStatus(run.Status), nil
	}
}

func finalizeRuns(home string, runIDs []string, reason string) error {
	p := PathsAt(home)
	if home == "" {
		var err error
		p, err = DefaultPaths()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(p.StateDB()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	return st.FinishRunsIfActive(context.Background(), runIDs, "cancelled", reason)
}

func finalizeRun(home, runID, reason string) error {
	p := PathsAt(home)
	if home == "" {
		var err error
		p, err = DefaultPaths()
		if err != nil {
			return err
		}
	}
	if _, err := os.Stat(p.StateDB()); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	st, err := store.Open(p.StateDB())
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	if isTerminalStatus(run.Status) {
		return nil
	}
	return st.FinishRun(ctx, runID, "cancelled", reason)
}
