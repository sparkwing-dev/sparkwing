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
	return runWingdRunWith(args, nil)
}

func runWingdRunWith(args []string, open func(context.Context, string) (wingd.LocalController, error)) error {
	fs := flag.NewFlagSet("wingd run", flag.ContinueOnError)
	home := fs.String("home", "", "sparkwing home (default: $SPARKWING_HOME or ~/.sparkwing)")
	version := fs.String("version", "", "binary version to advertise (default: this build)")
	headroom := fs.Float64("headroom", 0, "reserved host capacity fraction (0..1); 0 uses the default margin")
	budget := fs.String("budget", "", "machine budget cap (default: admission.budget in config.yaml); e.g. 6, 50%, 6,8gb, 50%,enforce")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("wingd run accepts only flags")
	}
	if open == nil {
		return errors.New("local controller composition is unavailable: shared controller authorization, store recovery and trigger hosting must be configured")
	}

	v := *version
	if v == "" {
		v = installedVersion()
	}

	resolvedBudget, err := wingd.ResolveBudget(*budget)
	if err != nil {
		return err
	}
	admissionPolicy, admissionSource, err := wingd.ResolveAdmissionPolicy()
	if err != nil {
		return err
	}

	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtime, err := wingd.NewLocalRuntime(wingd.LocalRuntimeConfig{
		Admission: wingd.Config{
			Home:               *home,
			HeadroomFraction:   *headroom,
			Budget:             resolvedBudget.Budget,
			BudgetSource:       resolvedBudget.Source,
			BudgetOrigin:       resolvedBudget.Origin,
			AdmissionPolicy:    &admissionPolicy,
			StoreSchemaVersion: store.ExpectedSchemaVersion(),
			Logf:               logger.Printf,
		},
		Open: open,
	})
	if err != nil {
		return err
	}
	logger.Printf("admission mode %s (%s)", admissionPolicy.Mode, admissionSource)
	return runtime.Run(ctx, wingd.LocalStartOptions{Home: *home, Version: v})
}
