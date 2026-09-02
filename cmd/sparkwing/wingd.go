package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/supervise"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

func runWingd(args []string) error {
	if len(args) == 0 {
		return errors.New("wingd: subcommand required (run, supervise)")
	}
	switch args[0] {
	case "run":
		return runWingdRun(args[1:])
	case "supervise":
		return supervise.Run(args[1:])
	default:
		return fmt.Errorf("wingd: unknown subcommand %q", args[0])
	}
}

func runWingdRun(args []string) error {
	fs := flag.NewFlagSet("wingd run", flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home (default: $SPARKWING_HOME or ~/.sparkwing)")
	version := fs.String("version", "", "binary version to advertise (default: this build)")
	headroom := fs.Float64("headroom", 0, "reserved host capacity fraction (0..1); 0 uses the default margin")
	budget := fs.String("budget", "", "machine budget cap (default: $SPARKWING_BUDGET, then the budget config file); e.g. 6, 50%, 6,8gb, 50%,enforce")
	if err := fs.Parse(args); err != nil {
		return err
	}

	v := *version
	if v == "" {
		v = os.Getenv("SPARKWING_WINGD_VERSION")
	}
	if v == "" {
		v = installedVersion()
	}

	resolvedBudget, err := wingd.ResolveBudget(*budget)
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	d, err := wingd.New(wingd.Config{
		Home:                  *home,
		Version:               v,
		HeadroomFraction:      *headroom,
		Budget:                resolvedBudget.Budget,
		BudgetSource:          resolvedBudget.Source,
		BudgetOrigin:          resolvedBudget.Origin,
		FinalizeRun:           orchestrator.NewOrphanRunFinalizer(*home),
		FinalizeCancelledRuns: orchestrator.NewCancelledRunsFinalizer(*home),
		IsRunTerminal:         orchestrator.NewTerminalRunChecker(*home),
		StoreSchemaVersion:    store.ExpectedSchemaVersion(),
		Logf:                  func(format string, args ...any) { logger.Printf(format, args...) },
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
