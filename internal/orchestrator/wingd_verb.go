package orchestrator

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/internal/wingd/supervise"
)

func RunWingd(args []string) error {
	if len(args) > 0 && args[0] == "supervise" {
		return supervise.Run(args[1:])
	}
	return runWingdCLI(args)
}

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
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	art, err := resolveArtifactStoreFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("wingd run: cache backend: %w", err)
	}
	return RunWingdDaemon(ctx, WingdOptions{
		Home:          *home,
		Version:       v,
		Budget:        resolvedBudget.Budget,
		BudgetSource:  resolvedBudget.Source,
		BudgetOrigin:  resolvedBudget.Origin,
		ArtifactStore: art,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "%s wingd: %s\n",
				time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
		},
	})
}
