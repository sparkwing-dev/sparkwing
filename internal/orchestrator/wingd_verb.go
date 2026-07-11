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

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

// runWingdCLI serves the hidden `<binary> wingd run` subcommand of
// compiled pipeline binaries. The wingd client library spawns the local
// admission daemon by re-execing the current binary, so any binary that
// requests admission must also be able to serve it.
func runWingdCLI(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: wingd run [--home DIR] [--version V]")
	}
	fs := flag.NewFlagSet("wingd run", flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home (default: $SPARKWING_HOME or ~/.sparkwing)")
	version := fs.String("version", "", "binary version to advertise (default: the compiled SDK version)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	v := *version
	if v == "" {
		v = sparkwingModuleVersion()
	}
	d, err := wingd.New(wingd.Config{
		Home:        *home,
		Version:     v,
		FinalizeRun: NewOrphanRunFinalizer(*home),
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "wingd: "+format+"\n", a...)
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
		if err := finalizeOrphanRun(home, runID); err != nil {
			slog.Warn("wingd: finalize orphaned run", "run_id", runID, "err", err)
		}
	}
}

func finalizeOrphanRun(home, runID string) error {
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
	return st.FinishRun(ctx, runID, "cancelled",
		"interrupted: run process exited without finalizing (admission connection lost)")
}
