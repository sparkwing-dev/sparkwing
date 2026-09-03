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
	"github.com/sparkwing-dev/sparkwing/pkg/storage"
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
	art, artFault := WingdArtifactStore(ctx)
	return RunWingdDaemon(ctx, WingdOptions{
		Home:               *home,
		Version:            v,
		Budget:             resolvedBudget.Budget,
		BudgetSource:       resolvedBudget.Source,
		BudgetOrigin:       resolvedBudget.Origin,
		ArtifactStore:      art,
		ArtifactStoreError: artFault,
		Logf: func(format string, a ...any) {
			fmt.Fprintf(os.Stderr, "%s wingd: %s\n",
				time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
		},
	})
}

// WingdArtifactStore resolves the artifact store the daemon's controller API
// serves artifact routes from, and reports why it could not as a string
// rather than an error.
//
// safety: a cache URL that will not open leaves the artifact routes
// unregistered; it is not a reason to leave the machine with no daemon, which
// would stop every run including those that touch no artifact.
func WingdArtifactStore(ctx context.Context) (storage.ArtifactStore, string) {
	art, err := resolveArtifactStoreFromEnv(ctx)
	if err != nil {
		return nil, err.Error()
	}
	return art, ""
}
