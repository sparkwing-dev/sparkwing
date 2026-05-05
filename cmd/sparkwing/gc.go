package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	flag "github.com/spf13/pflag"

	"github.com/sparkwing-dev/sparkwing/controller/client"
	"github.com/sparkwing-dev/sparkwing/orchestrator"
)

// runGC implements `sparkwing gc` -- manual invocation of the warm-PVC
// sweep. Normally fires at `wing runner` startup; exposed as a
// subcommand so operators can trigger it against a running pod via
// `kubectl exec` during incident response.
func runGC(args []string) error {
	fs := flag.NewFlagSet(cmdGC.Path, flag.ContinueOnError)
	root := fs.String("root", "",
		"warm-PVC root (default: SPARKWING_HOME resolution via DefaultPaths)")
	on := fs.String("on", "",
		"profile name (optional; without it the run-dir sweep is skipped)")
	if err := parseAndCheck(cmdGC, fs, args); err != nil {
		if errors.Is(err, errHelpRequested) {
			return nil
		}
		return err
	}

	if *root == "" {
		paths, err := orchestrator.DefaultPaths()
		if err != nil {
			return err
		}
		*root = paths.Root
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var ctrl orchestrator.TerminalRunLister
	// Only attempt profile resolution when the operator asked for it.
	// A completely profile-less gc run is legitimate for mtime-only
	// sweeps (git/, tmp/); the run-dir sweep just skips quietly.
	if *on != "" {
		prof, err := resolveProfile(*on)
		if err != nil {
			return err
		}
		if err := requireController(prof, "gc"); err != nil {
			return err
		}
		ctrl = client.NewWithToken(prof.Controller, nil, prof.Token)
	}

	logger := slog.Default()
	stats, err := orchestrator.GCWarmRoot(ctx, *root, ctrl, logger)
	if err != nil {
		return err
	}
	fmt.Printf("gc: git_dirs=%d tmp_entries=%d run_dirs=%d bytes_freed=%d root=%s\n",
		stats.GitDirsRemoved, stats.TmpEntriesRemoved, stats.RunDirsRemoved, stats.BytesFreed, *root)
	return nil
}
