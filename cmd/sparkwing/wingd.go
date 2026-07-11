// `sparkwing wingd run` -- the resident local admission arbiter. This
// verb is hidden: users never invoke it directly. The client library
// spawns it on demand as a detached process, and because it is the same
// binary as the CLI, the daemon and its clients can never skew versions.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/internal/orchestrator"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
)

// runWingd dispatches the hidden `sparkwing wingd <verb>` surface.
func runWingd(args []string) error {
	if len(args) == 0 {
		return errors.New("wingd: subcommand required (run)")
	}
	switch args[0] {
	case "run":
		return runWingdRun(args[1:])
	default:
		return fmt.Errorf("wingd: unknown subcommand %q", args[0])
	}
}

// runWingdRun elects and serves one daemon for a sparkwing home, blocking
// until it drains, idles out, or is signalled. Losing the election is a
// success: another daemon already serves the home.
func runWingdRun(args []string) error {
	fs := flag.NewFlagSet("wingd run", flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home (default: $SPARKWING_HOME or ~/.sparkwing)")
	version := fs.String("version", "", "binary version to advertise (default: this build)")
	headroom := fs.Float64("headroom", 0, "reserved host capacity fraction (0..1); 0 uses the default margin")
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

	d, err := wingd.New(wingd.Config{
		Home:             *home,
		Version:          v,
		HeadroomFraction: *headroom,
		FinalizeRun:      orchestrator.NewOrphanRunFinalizer(*home),
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
