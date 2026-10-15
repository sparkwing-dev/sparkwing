package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sparkwing-dev/sparkwing/internal/apierr"
	"github.com/sparkwing-dev/sparkwing/internal/clisurface"
	"github.com/sparkwing-dev/sparkwing/internal/paths"
	"github.com/sparkwing-dev/sparkwing/internal/wingd"
	"github.com/sparkwing-dev/sparkwing/pkg/store"
)

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

func runWingdRun(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runWingdRunCommand(ctx, args, os.Stdout, wingd.LocalStartOptions{Version: installedVersion()}, func(ctx context.Context) (wingd.LocalRuntime, error) {
		services, err := resolveLocalServices(ctx)
		return services.runtime, err
	})
}

func runWingdRunCommand(ctx context.Context, args []string, out io.Writer, options wingd.LocalStartOptions, resolve func(context.Context) (wingd.LocalRuntime, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"wingd", "run"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), out, inv.Output(false))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	options, err = foregroundOptions(inv, options)
	if err != nil {
		return err
	}
	runtime, err := resolve(ctx)
	if err != nil {
		return err
	}
	if runtime == nil {
		return &apierr.Error{Code: apierr.Unavailable, Message: "foreground local controller runtime is unavailable"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return runtime.Run(ctx, options)
}

func foregroundOptions(inv clisurface.Invocation, options wingd.LocalStartOptions) (wingd.LocalStartOptions, error) {
	for _, name := range []string{"home", "version", "standalone-reason"} {
		if inv.Has(name) && strings.TrimSpace(inv.Value(name)) == "" {
			return options, &clisurface.UsageError{Path: inv.Command.Path, Message: "--" + name + " requires a nonempty value"}
		}
	}
	for _, name := range []string{"budget", "headroom", "state-db"} {
		if inv.Has(name) {
			return options, &apierr.Error{Code: apierr.Unavailable, Message: "foreground runtime cannot receive --" + name, Hint: "the local runtime contract requires this option before it can be used"}
		}
	}
	if inv.Has("home") {
		options.Home = inv.Value("home")
	}
	if options.Home == "" {
		defaults, err := paths.DefaultPaths()
		if err != nil {
			return options, err
		}
		options.Home = defaults.Root
	}
	home, err := filepath.Abs(options.Home)
	if err != nil {
		return options, err
	}
	options.Home = home
	if options.Executable == "" {
		options.Executable, err = os.Executable()
		if err != nil {
			return options, err
		}
	}
	if inv.Has("version") {
		options.Version = inv.Value("version")
	}
	if inv.Has("stay") {
		options.Stay = inv.Bool("stay")
	}
	options.Standalone = options.Standalone || inv.Has("standalone-reason")
	return options, nil
}

func runWingdRunWith(args []string, open func(context.Context, string) (wingd.LocalController, error)) error {
	inv, err := clisurface.Parse("sparkwing", append([]string{"wingd", "run"}, args...))
	if err != nil {
		return err
	}
	if inv.Help() {
		return writeCommandHelp(surfaceHelp(inv.Command), os.Stdout, inv.Output(false))
	}
	if open == nil {
		return errors.New("local controller composition is unavailable: shared controller authorization, store recovery and trigger hosting must be configured")
	}
	options, err := foregroundOptions(inv, wingd.LocalStartOptions{Version: installedVersion()})
	if err != nil {
		return err
	}
	budget, err := wingd.ResolveBudget("")
	if err != nil {
		return err
	}
	policy, source, err := wingd.ResolveAdmissionPolicy()
	if err != nil {
		return err
	}
	logger := log.New(os.Stderr, "", log.LstdFlags|log.LUTC)
	runtime, err := wingd.NewLocalRuntime(wingd.LocalRuntimeConfig{
		Admission: wingd.Config{Home: options.Home, Budget: budget.Budget, BudgetSource: budget.Source, BudgetOrigin: budget.Origin, AdmissionPolicy: &policy, StoreSchemaVersion: store.ExpectedSchemaVersion(), Logf: logger.Printf},
		Open:      open,
	})
	if err != nil {
		return err
	}
	logger.Printf("admission mode %s (%s)", policy.Mode, source)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runtime.Run(ctx, options)
}
